package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

// User is an operator account.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
	TOTPSecret   string
	TOTPEnrolled bool
	FailedCount  int
	LockedUntil  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Locked reports whether the account is currently barred from logging in.
func (u *User) Locked(now time.Time) bool {
	return u.LockedUntil != nil && u.LockedUntil.After(now)
}

// CreateUser inserts an operator account.
func (s *Store) CreateUser(ctx context.Context, u *User) error {
	now := time.Now().UTC()
	u.CreatedAt, u.UpdatedAt = now, now

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, totp_secret, totp_enrolled,
			failed_count, locked_until, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		u.Username, u.PasswordHash, u.TOTPSecret, u.TOTPEnrolled,
		u.FailedCount, nullTime(u.LockedUntil), u.CreatedAt, u.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("username %q %w", u.Username, ErrConflict)
		}
		return fmt.Errorf("insert user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("user id: %w", err)
	}
	u.ID = id
	return nil
}

const userColumns = `id, username, password_hash, totp_secret, totp_enrolled,
	failed_count, locked_until, created_at, updated_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var locked sql.NullTime
	if err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.TOTPSecret,
		&u.TOTPEnrolled, &u.FailedCount, &locked, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}
	if locked.Valid {
		u.LockedUntil = &locked.Time
	}
	return &u, nil
}

// UserByUsername looks up an account for login.
func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE username = ?`, username)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("user %q: %w", username, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	return u, nil
}

// UserByID looks up an account by ID.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("user %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	return u, nil
}

// CountUsers reports how many accounts exist, used to decide whether the
// first-run setup flow should be offered.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// SetUserPassword replaces the stored password hash.
func (s *Store) SetUserPassword(ctx context.Context, id int64, hash string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
		hash, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("set password: %w", err)
	}
	return checkAffected(res, "user", id)
}

// SetUserTOTP stores the TOTP secret and enrollment state.
func (s *Store) SetUserTOTP(ctx context.Context, id int64, secret string, enrolled bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = ?, totp_enrolled = ?, updated_at = ? WHERE id = ?`,
		secret, enrolled, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("set totp: %w", err)
	}
	return checkAffected(res, "user", id)
}

// RecordLoginFailure increments the failure counter and locks the account
// once the threshold is reached.
func (s *Store) RecordLoginFailure(ctx context.Context, id int64, threshold int, lockFor time.Duration) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT failed_count FROM users WHERE id = ?`, id).Scan(&count); err != nil {
			return fmt.Errorf("read failure count: %w", err)
		}
		count++

		var lockedUntil any
		if count >= threshold {
			lockedUntil = time.Now().UTC().Add(lockFor)
			count = 0
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET failed_count = ?, locked_until = ?, updated_at = ? WHERE id = ?`,
			count, lockedUntil, time.Now().UTC(), id); err != nil {
			return fmt.Errorf("record failure: %w", err)
		}
		return nil
	})
}

// ResetLoginFailures clears the counter after a successful login.
func (s *Store) ResetLoginFailures(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET failed_count = 0, locked_until = NULL, updated_at = ? WHERE id = ?`,
		time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("reset failures: %w", err)
	}
	return nil
}

// CreateSession stores an opaque session token.
func (s *Store) CreateSession(ctx context.Context, token string, userID int64, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, expires_at, created_at) VALUES (?,?,?,?)`,
		token, userID, expiresAt.UTC(), time.Now().UTC())
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// SessionUser resolves a session token to its account, rejecting expired ones.
func (s *Store) SessionUser(ctx context.Context, token string) (*User, error) {
	var userID int64
	var expires time.Time
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, expires_at FROM sessions WHERE token = ?`, token).Scan(&userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	if time.Now().UTC().After(expires) {
		// Drop the dead row opportunistically; a failure here is not fatal to
		// the caller, which only needs to know the session is invalid.
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
		return nil, ErrNotFound
	}
	return s.UserByID(ctx, userID)
}

// DeleteSession revokes one session.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token = ?`, token)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// PurgeExpiredSessions removes sessions past their expiry.
func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < ?`, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("purge sessions: %w", err)
	}
	return nil
}

// AppendAudit records an operator action.
func (s *Store) AppendAudit(ctx context.Context, entry *model.AuditLog) error {
	if entry.TS.IsZero() {
		entry.TS = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_logs (ts, actor, action, target, detail, ip) VALUES (?,?,?,?,?,?)`,
		entry.TS, entry.Actor, entry.Action, entry.Target, entry.Detail, entry.IP)
	if err != nil {
		return fmt.Errorf("append audit: %w", err)
	}
	return nil
}

// ListAudit returns the most recent audit entries.
func (s *Store) ListAudit(ctx context.Context, limit int) ([]*model.AuditLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, actor, action, target, detail, ip FROM audit_logs ORDER BY ts DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query audit: %w", err)
	}
	defer rows.Close()

	var out []*model.AuditLog
	for rows.Next() {
		var a model.AuditLog
		if err := rows.Scan(&a.ID, &a.TS, &a.Actor, &a.Action, &a.Target, &a.Detail, &a.IP); err != nil {
			return nil, fmt.Errorf("scan audit: %w", err)
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}
