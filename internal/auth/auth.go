// Package auth handles operator login: password verification, mandatory TOTP,
// lockout after repeated failures, and opaque session tokens.
//
// The panel holds root credentials for every managed node, so authentication
// is treated as a perimeter, not a formality.
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/store"
)

const (
	// SessionTTL bounds how long a login stays valid.
	SessionTTL = 12 * time.Hour
	// sessionTokenBytes is the entropy of a session token.
	sessionTokenBytes = 32

	// LockThreshold is how many consecutive failures trigger a lockout.
	LockThreshold = 5
	// LockDuration is how long an account stays locked.
	LockDuration = 15 * time.Minute

	issuer = "fluxlite"
)

var (
	ErrInvalidCredentials = errors.New("invalid username, password or code")
	ErrAccountLocked      = errors.New("account is temporarily locked after repeated failures")
	ErrTOTPRequired       = errors.New("two-factor enrollment is required before login")
	ErrSetupClosed        = errors.New("initial setup has already been completed")
	ErrNoSession          = errors.New("no valid session")
)

// Service performs authentication against the store.
type Service struct {
	store *store.Store
}

func New(s *store.Store) *Service {
	return &Service{store: s}
}

// SetupNeeded reports whether no operator account exists yet.
func (s *Service) SetupNeeded(ctx context.Context) (bool, error) {
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// Enrollment carries the data needed to add an account to an authenticator app.
type Enrollment struct {
	Secret string `json:"secret"`
	URL    string `json:"url"`
}

// CreateInitialUser provisions the first operator account and returns its TOTP
// enrollment. It refuses to run once any account exists, so the setup endpoint
// cannot be used to add a backdoor account later.
func (s *Service) CreateInitialUser(ctx context.Context, username, password string) (*Enrollment, error) {
	needed, err := s.SetupNeeded(ctx)
	if err != nil {
		return nil, err
	}
	if !needed {
		return nil, ErrSetupClosed
	}
	if err := validateCredentialStrength(username, password); err != nil {
		return nil, err
	}

	hash, err := cryptox.HashPassword(password)
	if err != nil {
		return nil, err
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: username})
	if err != nil {
		return nil, fmt.Errorf("generate totp secret: %w", err)
	}

	user := &store.User{
		Username:     username,
		PasswordHash: hash,
		TOTPSecret:   key.Secret(),
		TOTPEnrolled: false,
	}
	if err := s.store.CreateUser(ctx, user); err != nil {
		return nil, err
	}
	return &Enrollment{Secret: key.Secret(), URL: key.URL()}, nil
}

// ConfirmEnrollment validates the first TOTP code and marks the account ready.
func (s *Service) ConfirmEnrollment(ctx context.Context, username, code string) error {
	user, err := s.store.UserByUsername(ctx, username)
	if err != nil {
		return ErrInvalidCredentials
	}
	if user.TOTPEnrolled {
		return ErrSetupClosed
	}
	if !totp.Validate(code, user.TOTPSecret) {
		return ErrInvalidCredentials
	}
	return s.store.SetUserTOTP(ctx, user.ID, user.TOTPSecret, true)
}

// Login verifies credentials and returns a session token.
func (s *Service) Login(ctx context.Context, username, password, code string) (string, time.Time, error) {
	now := time.Now().UTC()

	user, err := s.store.UserByUsername(ctx, username)
	if err != nil {
		// Spend comparable effort on unknown users so response time does not
		// reveal which usernames exist.
		_, _ = cryptox.HashPassword(password)
		return "", time.Time{}, ErrInvalidCredentials
	}
	if user.Locked(now) {
		return "", time.Time{}, ErrAccountLocked
	}
	if !user.TOTPEnrolled {
		return "", time.Time{}, ErrTOTPRequired
	}

	passwordOK, err := cryptox.VerifyPassword(password, user.PasswordHash)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("verify password: %w", err)
	}
	codeOK, err := totp.ValidateCustom(code, user.TOTPSecret, now, totp.ValidateOpts{
		Period: 30,
		// One period of drift each way tolerates ordinary clock skew without
		// meaningfully widening the window for a stolen code.
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	// A malformed code is a failed attempt, not a server error; it still has
	// to count towards the lockout below.
	if err != nil {
		codeOK = false
	}

	if !passwordOK || !codeOK {
		if ferr := s.store.RecordLoginFailure(ctx, user.ID, LockThreshold, LockDuration); ferr != nil {
			return "", time.Time{}, fmt.Errorf("record login failure: %w", ferr)
		}
		return "", time.Time{}, ErrInvalidCredentials
	}

	if err := s.store.ResetLoginFailures(ctx, user.ID); err != nil {
		return "", time.Time{}, fmt.Errorf("reset failures: %w", err)
	}

	token, err := cryptox.RandomToken(sessionTokenBytes)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := now.Add(SessionTTL)
	if err := s.store.CreateSession(ctx, token, user.ID, expires); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// Authenticate resolves a session token to its account.
func (s *Service) Authenticate(ctx context.Context, token string) (*store.User, error) {
	if token == "" {
		return nil, ErrNoSession
	}
	user, err := s.store.SessionUser(ctx, token)
	if err != nil {
		return nil, ErrNoSession
	}
	return user, nil
}

// Logout revokes a session.
func (s *Service) Logout(ctx context.Context, token string) error {
	return s.store.DeleteSession(ctx, token)
}

// ChangePassword updates the password after verifying the current one.
func (s *Service) ChangePassword(ctx context.Context, userID int64, current, next string) error {
	user, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	ok, err := cryptox.VerifyPassword(current, user.PasswordHash)
	if err != nil {
		return fmt.Errorf("verify password: %w", err)
	}
	if !ok {
		return ErrInvalidCredentials
	}
	if err := validateCredentialStrength(user.Username, next); err != nil {
		return err
	}
	hash, err := cryptox.HashPassword(next)
	if err != nil {
		return err
	}
	return s.store.SetUserPassword(ctx, userID, hash)
}

const minPasswordLength = 12

// validateCredentialStrength enforces a floor on password quality. The panel
// is internet-facing by design, so a short password is not the operator's
// private risk to take.
func validateCredentialStrength(username, password string) error {
	if len(username) < 3 {
		return errors.New("username must be at least 3 characters")
	}
	if len([]rune(password)) < minPasswordLength {
		return fmt.Errorf("password must be at least %d characters", minPasswordLength)
	}
	if password == username {
		return errors.New("password must not equal the username")
	}
	return nil
}
