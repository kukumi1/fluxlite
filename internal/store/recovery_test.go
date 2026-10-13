package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func seedRecoveryStore(t *testing.T) (*Store, *User, *UserRecovery) {
	t.Helper()
	st := openTemp(t)
	u := &User{Username: "admin", PasswordHash: "old-password-hash", TOTPSecret: "secret", TOTPEnrolled: true}
	if err := st.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	recovery := &UserRecovery{Question: "Secret question", AnswerHash: "answer-hash", CodeHash: make([]byte, 32)}
	if err := st.SetUserRecovery(context.Background(), u, nil, recovery); err != nil {
		t.Fatal(err)
	}
	return st, u, recovery
}

func TestRecoveryRejectsStaleProof(t *testing.T) {
	for _, mutation := range []string{"password", "totp", "username", "rotation", "deletion", "lock"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			st, u, recovery := seedRecoveryStore(t)
			var err error
			switch mutation {
			case "password":
				err = st.SetUserPassword(ctx, u.ID, "changed")
			case "totp":
				err = st.SetUserTOTP(ctx, u.ID, "new-secret", true)
			case "username":
				err = st.SetUsername(ctx, u.ID, "new-admin")
			case "rotation":
				next := &UserRecovery{Question: "Changed question", AnswerHash: "different", CodeHash: make([]byte, 32)}
				err = st.SetUserRecovery(ctx, u, recovery, next)
			case "deletion":
				err = st.SetUserRecovery(ctx, u, recovery, nil)
			case "lock":
				err = st.RecordLoginFailure(ctx, u.ID, 1, time.Minute)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := st.RecoverUserPassword(ctx, u, recovery, "reset-hash"); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale reset accepted: %v", err)
			}
			if err := st.SetUserRecovery(ctx, u, recovery, recovery); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale configuration accepted: %v", err)
			}
		})
	}
}

func TestRecoverySingleUseConcurrentReset(t *testing.T) {
	ctx := context.Background()
	st, u, recovery := seedRecoveryStore(t)
	start := make(chan struct{})
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- st.RecoverUserPassword(ctx, u, recovery, "reset-hash")
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("reset used %d times", success)
	}
}

func TestRecoveryConcurrentRotationAllowsOnlyOneProof(t *testing.T) {
	ctx := context.Background()
	st, u, recovery := seedRecoveryStore(t)
	next := &UserRecovery{Question: "Rotated", AnswerHash: "new", CodeHash: make([]byte, 32)}
	results := make(chan error, 2)
	start := make(chan struct{})
	go func() { <-start; results <- st.SetUserRecovery(ctx, u, recovery, next) }()
	go func() { <-start; results <- st.RecoverUserPassword(ctx, u, recovery, "reset") }()
	close(start)
	a, b := <-results, <-results
	if (a == nil) == (b == nil) {
		t.Fatalf("expected exactly one operation to win: %v / %v", a, b)
	}
	if a != nil && !errors.Is(a, ErrConflict) || b != nil && !errors.Is(b, ErrConflict) {
		t.Fatalf("unexpected failure: %v / %v", a, b)
	}
}

func TestRecoveryRollbackIsAtomic(t *testing.T) {
	ctx := context.Background()
	st, u, recovery := seedRecoveryStore(t)
	if err := st.CreateSession(ctx, "live-token", u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`CREATE TRIGGER reject_session_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(ABORT, 'failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := st.RecoverUserPassword(ctx, u, recovery, "new-hash"); err == nil {
		t.Fatal("expected transaction failure")
	}
	current, err := st.UserByID(ctx, u.ID)
	if err != nil || current.PasswordHash != u.PasswordHash {
		t.Fatalf("password not rolled back: %v", err)
	}
	stored, err := st.UserRecovery(ctx, u.ID)
	if err != nil || !recoveryMatches(stored, recovery) {
		t.Fatalf("recovery not rolled back: %v", err)
	}
	if _, err := st.SessionUser(ctx, "live-token"); err != nil {
		t.Fatal("session not rolled back", err)
	}
}

func TestRecoveryMigrationFrom43(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	u := &User{Username: "existing-admin", PasswordHash: "existing-hash"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DROP TABLE user_recovery; UPDATE schema_version SET version=43`); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var version int
	if err := st.db.QueryRow(`SELECT version FROM schema_version`).Scan(&version); err != nil || version != 44 {
		t.Fatalf("migration version %d: %v", version, err)
	}
	current, err := st.UserByID(ctx, u.ID)
	if err != nil || current.PasswordHash != u.PasswordHash {
		t.Fatalf("existing user changed: %v", err)
	}
	recovery, err := st.UserRecovery(ctx, u.ID)
	if err != nil || recovery != nil {
		t.Fatalf("existing account recovery must default off: %v", err)
	}
}

func TestRecoveryRejectsInFlightOldPasswordLogin(t *testing.T) {
	ctx := context.Background()
	st, oldLoginProof, recovery := seedRecoveryStore(t)
	if err := st.CreateSessionIfCredentialsMatch(ctx, oldLoginProof, "before-reset", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.RecoverUserPassword(ctx, oldLoginProof, recovery, "new-password-hash"); err != nil {
		t.Fatal(err)
	}
	// Represents Login after its expensive password verification has already
	// finished, but before session insertion. Recovery won the intervening race.
	if err := st.CreateSessionIfCredentialsMatch(ctx, oldLoginProof, "after-reset", time.Now().Add(time.Hour)); !errors.Is(err, ErrConflict) {
		t.Fatal("old password proof resurrected a session", err)
	}
	count, err := st.CountUserSessions(ctx, oldLoginProof.ID)
	if err != nil || count != 0 {
		t.Fatal("sessions survived reset", count, err)
	}
	newProof, err := st.UserByID(ctx, oldLoginProof.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSessionIfCredentialsMatch(ctx, newProof, "new-password-login", time.Now().Add(time.Hour)); err != nil {
		t.Fatal("fresh password proof rejected", err)
	}
}

func TestLoginCredentialCASRejectsChangedTOTPAndLock(t *testing.T) {
	for _, mutation := range []string{"totp", "username", "lock"} {
		t.Run(mutation, func(t *testing.T) {
			ctx := context.Background()
			st, proof, _ := seedRecoveryStore(t)
			var err error
			switch mutation {
			case "totp":
				err = st.SetUserTOTP(ctx, proof.ID, "new-secret", true)
			case "username":
				err = st.SetUsername(ctx, proof.ID, "renamed")
			case "lock":
				err = st.RecordLoginFailure(ctx, proof.ID, 1, time.Minute)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := st.CreateSessionIfCredentialsMatch(ctx, proof, "stale-login", time.Now().Add(time.Hour)); !errors.Is(err, ErrConflict) {
				t.Fatal("stale login proof accepted", err)
			}
		})
	}
}
