package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"
)

// UserRecovery is never serialized. Only the question may leave the store;
// the answer and the offline recovery code are stored as one-way hashes.
type UserRecovery struct {
	Question   string `json:"-"`
	AnswerHash string `json:"-"`
	CodeHash   []byte `json:"-"`
}

func readRecovery(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID int64) (*UserRecovery, error) {
	var recovery UserRecovery
	err := db.QueryRowContext(ctx, `SELECT question, answer_hash, code_hash FROM user_recovery WHERE user_id = ?`, userID).
		Scan(&recovery.Question, &recovery.AnswerHash, &recovery.CodeHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &recovery, err
}

func (s *Store) UserRecovery(ctx context.Context, userID int64) (*UserRecovery, error) {
	return readRecovery(ctx, s.db, userID)
}

func recoveryMatches(a, b *UserRecovery) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Question == b.Question && a.AnswerHash == b.AnswerHash &&
		subtle.ConstantTimeCompare(a.CodeHash, b.CodeHash) == 1
}

// withRecoveryProof rejects a proof verified against superseded credentials
// or recovery settings. Rechecking under the write transaction prevents
// concurrent reset, rotation, TOTP changes and deletion from replaying it.
func (s *Store) withRecoveryProof(ctx context.Context, expected *User, recovery *UserRecovery, fn func(*sql.Tx) error) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		current, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, expected.ID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		if !credentialsMatch(current, expected) {
			return ErrConflict
		}
		stored, err := readRecovery(ctx, tx, expected.ID)
		if err != nil {
			return err
		}
		if !recoveryMatches(stored, recovery) {
			return ErrConflict
		}
		return fn(tx)
	})
}

// SetUserRecovery replaces (or removes) recovery settings after reauthentication.
func (s *Store) SetUserRecovery(ctx context.Context, expected *User, old, next *UserRecovery) error {
	return s.withRecoveryProof(ctx, expected, old, func(tx *sql.Tx) error {
		if next == nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM user_recovery WHERE user_id = ?`, expected.ID); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `INSERT INTO user_recovery (user_id, question, answer_hash, code_hash) VALUES (?,?,?,?)
				ON CONFLICT(user_id) DO UPDATE SET question=excluded.question, answer_hash=excluded.answer_hash, code_hash=excluded.code_hash`,
				expected.ID, next.Question, next.AnswerHash, next.CodeHash); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE users SET failed_count=0, locked_until=NULL, updated_at=? WHERE id=?`, time.Now().UTC(), expected.ID)
		return err
	})
}

// RecoverUserPassword consumes recovery settings, changes the password and
// revokes all sessions (including their console unlocks) in one transaction.
// The TOTP authenticator is deliberately not altered.
func (s *Store) RecoverUserPassword(ctx context.Context, expected *User, recovery *UserRecovery, passwordHash string) error {
	if recovery == nil {
		return ErrConflict
	}
	return s.withRecoveryProof(ctx, expected, recovery, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=?, failed_count=0, locked_until=NULL, updated_at=? WHERE id=?`,
			passwordHash, time.Now().UTC(), expected.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=?`, expected.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM user_recovery WHERE user_id=?`, expected.ID)
		return err
	})
}
