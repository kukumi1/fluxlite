package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/store"
)

var (
	ErrInvalidRecovery = errors.New("恢复验证失败，请检查信息或稍后重试")
	ErrRecoveryInput   = errors.New("invalid recovery input")
)

type recoveryInputError string

func (e recoveryInputError) Error() string { return string(e) }
func (e recoveryInputError) Unwrap() error { return ErrRecoveryInput }

type RecoveryStatus struct {
	Enabled  bool   `json:"enabled"`
	Question string `json:"question"`
}

type RecoveryQuestion struct {
	Question     string `json:"question"`
	TOTPRequired bool   `json:"totp_required"`
}

func recoveryDigest(code string) []byte {
	digest := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return digest[:]
}

func (s *Service) RecoveryStatus(ctx context.Context, userID int64) (*RecoveryStatus, error) {
	recovery, err := s.store.UserRecovery(ctx, userID)
	if err != nil {
		return nil, err
	}
	status := &RecoveryStatus{}
	if recovery != nil {
		status.Enabled, status.Question = true, recovery.Question
	}
	return status, nil
}

// recoveryReauthentication captures the account and recovery settings before
// doing expensive verification. The store rechecks both snapshots atomically.
func (s *Service) recoveryReauthentication(ctx context.Context, userID int64, password, code string) (*store.User, *store.UserRecovery, error) {
	u, err := s.store.UserByID(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	if u.Locked(time.Now().UTC()) {
		return nil, nil, ErrInvalidRecovery
	}
	recovery, err := s.store.UserRecovery(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	passwordOK, err := cryptox.VerifyPassword(password, u.PasswordHash)
	if err != nil {
		return nil, nil, err
	}
	codeOK := !u.TOTPEnrolled || validateTOTP(strings.TrimSpace(code), u.TOTPSecret, time.Now().UTC())
	if !passwordOK || !codeOK {
		return nil, nil, s.recoveryFailure(ctx, u.ID)
	}
	return u, recovery, nil
}

func (s *Service) ConfigureRecovery(ctx context.Context, userID int64, password, code, question, answer string) (string, error) {
	question, answer = strings.TrimSpace(question), strings.TrimSpace(answer)
	if !utf8.ValidString(question) || utf8.RuneCountInString(question) < 3 || utf8.RuneCountInString(question) > 120 {
		return "", recoveryInputError("安全问题须为 3–120 个字符")
	}
	if !utf8.ValidString(answer) || utf8.RuneCountInString(answer) < 4 || utf8.RuneCountInString(answer) > 256 {
		return "", recoveryInputError("安全答案须为 4–256 个字符，建议使用不公开的长短语")
	}
	u, old, err := s.recoveryReauthentication(ctx, userID, password, code)
	if err != nil {
		return "", err
	}
	answerHash, err := cryptox.HashPassword(answer)
	if err != nil {
		return "", err
	}
	recoveryCode, err := cryptox.RandomToken(32)
	if err != nil {
		return "", err
	}
	err = s.store.SetUserRecovery(ctx, u, old, &store.UserRecovery{Question: question, AnswerHash: answerHash, CodeHash: recoveryDigest(recoveryCode)})
	if errors.Is(err, store.ErrConflict) {
		return "", ErrInvalidRecovery
	}
	return recoveryCode, err
}

func (s *Service) DisableRecovery(ctx context.Context, userID int64, password, code string) error {
	u, old, err := s.recoveryReauthentication(ctx, userID, password, code)
	if err != nil {
		return err
	}
	err = s.store.SetUserRecovery(ctx, u, old, nil)
	if errors.Is(err, store.ErrConflict) {
		return ErrInvalidRecovery
	}
	return err
}

// Invalid offline codes cannot lock an account: doing so would let anyone who
// knows a username deny the owner access. Public endpoints are IP throttled;
// account lockout begins only after possession of the random code is proved.
func (s *Service) recoveryIdentity(ctx context.Context, username, recoveryCode string) (*store.User, *store.UserRecovery, error) {
	u, err := s.store.UserByUsername(ctx, strings.TrimSpace(username))
	if errors.Is(err, store.ErrNotFound) {
		// Follow the same query and digest path for unknown accounts.
		u, err = &store.User{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	recovery, err := s.store.UserRecovery(ctx, u.ID)
	if err != nil {
		return nil, nil, err
	}
	digest := recoveryDigest(recoveryCode)
	var expected [sha256.Size]byte
	if recovery != nil {
		copy(expected[:], recovery.CodeHash)
	}
	valid := subtle.ConstantTimeCompare(expected[:], digest) == 1
	if recovery == nil || !valid || u.ID == 0 || u.Locked(time.Now().UTC()) {
		return nil, nil, ErrInvalidRecovery
	}
	return u, recovery, nil
}

func (s *Service) GetRecoveryQuestion(ctx context.Context, username, recoveryCode string) (*RecoveryQuestion, error) {
	u, recovery, err := s.recoveryIdentity(ctx, username, recoveryCode)
	if err != nil {
		return nil, err
	}
	return &RecoveryQuestion{Question: recovery.Question, TOTPRequired: u.TOTPEnrolled}, nil
}

func (s *Service) recoveryFailure(ctx context.Context, userID int64) error {
	if err := s.store.RecordLoginFailure(ctx, userID, LockThreshold, LockDuration); err != nil {
		return fmt.Errorf("record recovery failure: %w", err)
	}
	return ErrInvalidRecovery
}

func (s *Service) ResetRecoveredPassword(ctx context.Context, username, recoveryCode, answer, code, password string) error {
	u, recovery, err := s.recoveryIdentity(ctx, username, recoveryCode)
	if err != nil {
		return err
	}
	answer = strings.TrimSpace(answer)
	if !utf8.ValidString(answer) || utf8.RuneCountInString(answer) > 256 {
		return s.recoveryFailure(ctx, u.ID)
	}
	answerOK, err := cryptox.VerifyPassword(answer, recovery.AnswerHash)
	if err != nil {
		return err
	}
	codeOK := !u.TOTPEnrolled || validateTOTP(strings.TrimSpace(code), u.TOTPSecret, time.Now().UTC())
	if !answerOK || !codeOK {
		return s.recoveryFailure(ctx, u.ID)
	}
	if err := validateCredentialStrength(u.Username, password); err != nil {
		return recoveryInputError("新密码须至少 12 个字符，且不能与用户名相同")
	}
	hash, err := cryptox.HashPassword(password)
	if err != nil {
		return err
	}
	err = s.store.RecoverUserPassword(ctx, u, recovery, hash)
	if errors.Is(err, store.ErrConflict) {
		return ErrInvalidRecovery
	}
	return err
}
