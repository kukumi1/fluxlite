package auth

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"github.com/kukumi1/fluxlite/internal/store"
)

const password = "correct-horse-battery"

func newService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st), st
}

func seedUser(t *testing.T, s *Service) *store.User {
	t.Helper()
	ctx := context.Background()
	if _, err := s.CreateInitialUser(ctx, "admin", password); err != nil {
		t.Fatalf("create user: %v", err)
	}
	u, err := s.store.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("read user: %v", err)
	}
	return u
}

func code(t *testing.T, secret string) string {
	t.Helper()
	c, err := totp.GenerateCode(secret, time.Now().UTC())
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	return c
}

// An account without a second factor logs in on the password alone. Demanding
// a code it does not have would lock the operator out permanently.
func TestLoginWithoutTOTP(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	seedUser(t, s)

	if _, _, err := s.Login(ctx, "admin", password, ""); err != nil {
		t.Fatalf("login without a second factor: %v", err)
	}
	if _, _, err := s.Login(ctx, "admin", "wrong-password-here", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("a wrong password was accepted: %v", err)
	}
}

// Turning the factor off must not be reachable from a stolen session alone.
func TestDisableTOTPRequiresPasswordAndCode(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	u := seedUser(t, s)

	if err := s.EnableTOTP(ctx, u.ID, code(t, u.TOTPSecret)); err != nil {
		t.Fatalf("enable: %v", err)
	}

	if err := s.DisableTOTP(ctx, u.ID, "wrong", code(t, u.TOTPSecret)); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("disabled with a wrong password: %v", err)
	}
	if err := s.DisableTOTP(ctx, u.ID, password, "000000"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("disabled with a wrong code: %v", err)
	}

	after, err := s.store.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("read user: %v", err)
	}
	if !after.TOTPEnrolled {
		t.Fatal("failed attempts turned the factor off anyway")
	}

	if err := s.DisableTOTP(ctx, u.ID, password, code(t, u.TOTPSecret)); err != nil {
		t.Fatalf("disable with correct credentials: %v", err)
	}
	after, err = s.store.UserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("read user: %v", err)
	}
	if after.TOTPEnrolled {
		t.Error("the factor is still enrolled after being disabled")
	}
	// A secret that may have been screenshotted must not come back on re-enable.
	if after.TOTPSecret != "" {
		t.Error("the old secret survived being disabled")
	}
}

// While enabled, the code is not optional.
func TestLoginWithTOTPRequiresCode(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	u := seedUser(t, s)

	if err := s.EnableTOTP(ctx, u.ID, code(t, u.TOTPSecret)); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if _, _, err := s.Login(ctx, "admin", password, ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("logged in without a code while enrolled: %v", err)
	}
	if _, _, err := s.Login(ctx, "admin", password, code(t, u.TOTPSecret)); err != nil {
		t.Errorf("correct password and code rejected: %v", err)
	}
}

// An abandoned enrollment must leave login exactly as it was.
func TestAbandonedEnrollmentDoesNotLockTheOperatorOut(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	u := seedUser(t, s)

	if _, err := s.BeginTOTPEnrollment(ctx, u.ID); err != nil {
		t.Fatalf("begin enrollment: %v", err)
	}
	if _, _, err := s.Login(ctx, "admin", password, ""); err != nil {
		t.Fatalf("a pending enrollment blocked login: %v", err)
	}
}

// Changing the password is how an operator responds to a suspected leak, so
// sessions opened with the old one must not outlive it.
func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t)
	u := seedUser(t, s)

	mine, _, err := s.Login(ctx, "admin", password, "")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	other, _, err := s.Login(ctx, "admin", password, "")
	if err != nil {
		t.Fatalf("second login: %v", err)
	}

	if err := s.ChangePassword(ctx, u.ID, password, "another-long-password", mine); err != nil {
		t.Fatalf("change password: %v", err)
	}

	if _, err := s.Authenticate(ctx, other); !errors.Is(err, ErrNoSession) {
		t.Error("a session opened with the old password survived")
	}
	if _, err := s.Authenticate(ctx, mine); err != nil {
		t.Errorf("the caller's own session was revoked: %v", err)
	}
	if n, err := st.CountUserSessions(ctx, u.ID); err != nil || n != 1 {
		t.Errorf("sessions = %d (err %v), want 1", n, err)
	}
}

func TestChangeUsernameRequiresPassword(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t)
	u := seedUser(t, s)

	if err := s.ChangeUsername(ctx, u.ID, "wrong", "kukumi"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("renamed with a wrong password: %v", err)
	}
	if err := s.ChangeUsername(ctx, u.ID, password, "kukumi"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, _, err := s.Login(ctx, "kukumi", password, ""); err != nil {
		t.Errorf("login under the new name: %v", err)
	}
}
