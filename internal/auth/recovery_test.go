package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/cryptox"
)

const recoveryAnswer = "A long private answer"

func TestRecoveryLifecycle(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t)
	u := seedUser(t, s)
	status, err := s.RecoveryStatus(ctx, u.ID)
	if err != nil || status.Enabled || status.Question != "" {
		t.Fatal("recovery enabled without opting in", status, err)
	}
	if _, err := s.ConfigureRecovery(ctx, u.ID, "wrong-password", "", "安全问题是什么？", recoveryAnswer); !errors.Is(err, ErrInvalidRecovery) {
		t.Fatal("configuration accepted stolen session", err)
	}
	key, err := s.ConfigureRecovery(ctx, u.ID, password, "", "  安全问题是什么？  ", "  "+recoveryAnswer+"  ")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(decoded) != 32 {
		t.Fatal("recovery code must have 256-bit entropy")
	}
	recovery, err := st.UserRecovery(ctx, u.ID)
	if err != nil || string(recovery.CodeHash) == key || recovery.AnswerHash == recoveryAnswer || !strings.HasPrefix(recovery.AnswerHash, "$argon2id$") {
		t.Fatal("stored recovery contains plaintext", err)
	}
	question, err := s.GetRecoveryQuestion(ctx, "admin", key)
	if err != nil || question.Question != "安全问题是什么？" || question.TOTPRequired {
		t.Fatal("question proof failed", question, err)
	}
	token, _, err := s.Login(ctx, "admin", password, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkConsoleUnlocked(ctx, token, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetRecoveredPassword(ctx, "admin", key, recoveryAnswer, "", "short"); !errors.Is(err, ErrRecoveryInput) {
		t.Fatal("weak password accepted", err)
	}
	if err := s.ResetRecoveredPassword(ctx, "admin", key, recoveryAnswer, "", "new-strong-password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatal("old session survived", err)
	}
	if unlocked, err := st.ConsoleUnlocked(ctx, token); err != nil || unlocked {
		t.Fatal("console unlock survived", err)
	}
	if _, err := s.GetRecoveryQuestion(ctx, "admin", key); !errors.Is(err, ErrInvalidRecovery) {
		t.Fatal("consumed key reused", err)
	}
	status, err = s.RecoveryStatus(ctx, u.ID)
	if err != nil || status.Enabled {
		t.Fatal("recovery not consumed", err)
	}
	if _, _, err := s.Login(ctx, "admin", "new-strong-password", ""); err != nil {
		t.Fatal("new password unusable", err)
	}
	current, err := st.UserByID(ctx, u.ID)
	if err != nil || current.TOTPSecret != u.TOTPSecret || current.TOTPEnrolled != u.TOTPEnrolled {
		t.Fatal("TOTP changed", err)
	}
}

func TestRecoveryRequiresAndPreservesTOTP(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t)
	u := seedUser(t, s)
	if err := s.EnableTOTP(ctx, u.ID, code(t, u.TOTPSecret)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfigureRecovery(ctx, u.ID, password, "", "Secret question", recoveryAnswer); !errors.Is(err, ErrInvalidRecovery) {
		t.Fatal("configuration omitted TOTP", err)
	}
	key, err := s.ConfigureRecovery(ctx, u.ID, password, code(t, u.TOTPSecret), "Secret question", recoveryAnswer)
	if err != nil {
		t.Fatal(err)
	}
	question, err := s.GetRecoveryQuestion(ctx, "admin", key)
	if err != nil || !question.TOTPRequired {
		t.Fatal("TOTP requirement absent", err)
	}
	if err := s.ResetRecoveredPassword(ctx, "admin", key, recoveryAnswer, "", "new-strong-password"); !errors.Is(err, ErrInvalidRecovery) {
		t.Fatal("reset omitted TOTP", err)
	}
	if err := s.DisableRecovery(ctx, u.ID, password, ""); !errors.Is(err, ErrInvalidRecovery) {
		t.Fatal("disable omitted TOTP", err)
	}
	if err := s.ResetRecoveredPassword(ctx, "admin", key, recoveryAnswer, code(t, u.TOTPSecret), "new-strong-password"); err != nil {
		t.Fatal(err)
	}
	current, err := st.UserByID(ctx, u.ID)
	if err != nil || !current.TOTPEnrolled || current.TOTPSecret != u.TOTPSecret {
		t.Fatal("TOTP disabled by password recovery", err)
	}
	if _, _, err := s.Login(ctx, "admin", "new-strong-password", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("new password bypassed TOTP", err)
	}
}

func TestRecoveryInvalidProofsAndLockout(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t)
	u := seedUser(t, s)
	key, err := s.ConfigureRecovery(ctx, u.ID, password, "", "Secret question", recoveryAnswer)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < LockThreshold+1; i++ {
		for _, username := range []string{"admin", "unknown"} {
			if _, err := s.GetRecoveryQuestion(ctx, username, "wrong"); !errors.Is(err, ErrInvalidRecovery) {
				t.Fatal("wrong recovery code accepted", err)
			}
		}
	}
	current, err := st.UserByID(ctx, u.ID)
	if err != nil || current.FailedCount != 0 || current.Locked(time.Now()) {
		t.Fatal("invalid random codes locked victim", err)
	}
	for i := 0; i < LockThreshold; i++ {
		if err := s.ResetRecoveredPassword(ctx, "admin", key, strings.ToLower(recoveryAnswer), "", "new-strong-password"); !errors.Is(err, ErrInvalidRecovery) {
			t.Fatal("wrong answer accepted", err)
		}
	}
	current, err = st.UserByID(ctx, u.ID)
	if err != nil || !current.Locked(time.Now()) {
		t.Fatal("known recovery code guesses did not lock", err)
	}
	if _, err := s.GetRecoveryQuestion(ctx, "admin", key); !errors.Is(err, ErrInvalidRecovery) {
		t.Fatal("locked account question revealed", err)
	}
	if err := s.ResetRecoveredPassword(ctx, "admin", key, recoveryAnswer, "", "new-strong-password"); !errors.Is(err, ErrInvalidRecovery) {
		t.Fatal("locked account recovered", err)
	}
	match, err := cryptox.VerifyPassword(password, current.PasswordHash)
	if err != nil || !match {
		t.Fatal("failed proof changed password", err)
	}
}

func TestRecoveryRotationAndRemoval(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	u := seedUser(t, s)
	key, err := s.ConfigureRecovery(ctx, u.ID, password, "", "Secret question", recoveryAnswer)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.ConfigureRecovery(ctx, u.ID, password, "", "Another question", recoveryAnswer)
	if err != nil || next == key {
		t.Fatal("rotation failed", err)
	}
	if _, err := s.GetRecoveryQuestion(ctx, "admin", key); !errors.Is(err, ErrInvalidRecovery) {
		t.Fatal("old key survived rotation", err)
	}
	if err := s.DisableRecovery(ctx, u.ID, "wrong", ""); !errors.Is(err, ErrInvalidRecovery) {
		t.Fatal("disable without password", err)
	}
	if err := s.DisableRecovery(ctx, u.ID, password, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRecoveryQuestion(ctx, "admin", next); !errors.Is(err, ErrInvalidRecovery) {
		t.Fatal("key survived removal", err)
	}
}

func TestRecoveryInputBounds(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	u := seedUser(t, s)
	for _, input := range []struct{ question, answer string }{
		{"", recoveryAnswer}, {"ab", recoveryAnswer}, {strings.Repeat("问", 121), recoveryAnswer},
		{"Secret question", "abc"}, {"Secret question", strings.Repeat("答", 257)},
	} {
		if _, err := s.ConfigureRecovery(ctx, u.ID, password, "", input.question, input.answer); !errors.Is(err, ErrRecoveryInput) {
			t.Fatal("invalid input accepted", err)
		}
	}
}
