package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

// MarkConsoleUnlocked records that this session has re-proven the password and
// may open terminals. The mark is scoped to the session, so it dies with it.
func (s *Store) MarkConsoleUnlocked(ctx context.Context, token string, at time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET console_unlocked_at = ? WHERE token = ? AND expires_at > ?`,
		at, token, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("mark console unlocked: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark console unlocked: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ConsoleUnlocked reports whether this session may open terminals.
//
// An expired session answers false rather than erroring: the caller's question
// is "may this request open a shell", and for an expired token the answer is
// simply no.
func (s *Store) ConsoleUnlocked(ctx context.Context, token string) (bool, error) {
	var at sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT console_unlocked_at FROM sessions WHERE token = ? AND expires_at > ?`,
		token, time.Now().UTC()).Scan(&at)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read console unlock: %w", err)
	}
	return at.Valid, nil
}

func (s *Store) ListConsoleCommands(ctx context.Context) ([]*model.ConsoleCommand, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, command, created_at FROM console_commands ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("query console commands: %w", err)
	}
	defer rows.Close()

	var out []*model.ConsoleCommand
	for rows.Next() {
		c := &model.ConsoleCommand{}
		if err := rows.Scan(&c.ID, &c.Name, &c.Command, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan console command: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CreateConsoleCommand(ctx context.Context, c *model.ConsoleCommand) error {
	c.CreatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO console_commands (name, command, created_at) VALUES (?,?,?)`,
		c.Name, c.Command, c.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert console command: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("console command id: %w", err)
	}
	c.ID = id
	return nil
}

func (s *Store) DeleteConsoleCommand(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM console_commands WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete console command: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete console command: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
