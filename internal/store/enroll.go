package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// EnrollToken is a one-shot credential that lets a machine register itself.
type EnrollToken struct {
	Token         string
	Name          string
	Host          string
	SSHPort       int
	SSHUser       string
	PortStart     int
	PortEnd       int
	ViaNodeID     *int64
	SkipUDPProbe  bool
	PrivateKey    []byte
	AuthorizedKey string
	ExpiresAt     time.Time
	UsedAt        *time.Time
	NodeID        *int64
	CreatedAt     time.Time
}

// Usable reports whether the token can still enroll a node.
func (t *EnrollToken) Usable(now time.Time) bool {
	return t.UsedAt == nil && now.Before(t.ExpiresAt)
}

// CreateEnrollToken stores a pending enrollment.
func (s *Store) CreateEnrollToken(ctx context.Context, t *EnrollToken) error {
	t.CreatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO enroll_tokens (token, name, host, ssh_port, ssh_user,
			port_start, port_end, via_node_id, skip_udp_probe, private_key,
			authorized_key, expires_at, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.Token, t.Name, t.Host, t.SSHPort, t.SSHUser, t.PortStart, t.PortEnd,
		nullInt64(t.ViaNodeID), t.SkipUDPProbe, t.PrivateKey, t.AuthorizedKey,
		t.ExpiresAt.UTC(), t.CreatedAt)
	if err != nil {
		return fmt.Errorf("create enroll token: %w", err)
	}
	return nil
}

// EnrollTokenByValue looks up a pending enrollment.
func (s *Store) EnrollTokenByValue(ctx context.Context, token string) (*EnrollToken, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT token, name, host, ssh_port, ssh_user, port_start, port_end,
			via_node_id, skip_udp_probe, private_key, authorized_key, expires_at,
			used_at, node_id, created_at
		FROM enroll_tokens WHERE token = ?`, token)

	var t EnrollToken
	var via, nodeID sql.NullInt64
	var used sql.NullTime
	err := row.Scan(&t.Token, &t.Name, &t.Host, &t.SSHPort, &t.SSHUser,
		&t.PortStart, &t.PortEnd, &via, &t.SkipUDPProbe, &t.PrivateKey,
		&t.AuthorizedKey, &t.ExpiresAt, &used, &nodeID, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan enroll token: %w", err)
	}
	if via.Valid {
		t.ViaNodeID = &via.Int64
	}
	if nodeID.Valid {
		t.NodeID = &nodeID.Int64
	}
	if used.Valid {
		t.UsedAt = &used.Time
	}
	return &t, nil
}

// MarkEnrollTokenUsed retires a token once it has produced a node.
func (s *Store) MarkEnrollTokenUsed(ctx context.Context, token string, nodeID int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE enroll_tokens SET used_at = ?, node_id = ? WHERE token = ? AND used_at IS NULL`,
		time.Now().UTC(), nodeID, token)
	if err != nil {
		return fmt.Errorf("mark enroll token used: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	// Zero rows means another request consumed the token first; treating that
	// as success would let one token enroll two nodes.
	if n == 0 {
		return fmt.Errorf("enroll token already used: %w", ErrConflict)
	}
	return nil
}

// ListPendingEnrollTokens returns tokens that have not been used or expired.
func (s *Store) ListPendingEnrollTokens(ctx context.Context) ([]*EnrollToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT token, name, host, ssh_port, ssh_user, port_start, port_end,
			via_node_id, skip_udp_probe, private_key, authorized_key, expires_at,
			used_at, node_id, created_at
		FROM enroll_tokens
		WHERE used_at IS NULL AND expires_at > ?
		ORDER BY created_at DESC`, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("query enroll tokens: %w", err)
	}
	defer rows.Close()

	var out []*EnrollToken
	for rows.Next() {
		var t EnrollToken
		var via, nodeID sql.NullInt64
		var used sql.NullTime
		if err := rows.Scan(&t.Token, &t.Name, &t.Host, &t.SSHPort, &t.SSHUser,
			&t.PortStart, &t.PortEnd, &via, &t.SkipUDPProbe, &t.PrivateKey,
			&t.AuthorizedKey, &t.ExpiresAt, &used, &nodeID, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan enroll token: %w", err)
		}
		if via.Valid {
			t.ViaNodeID = &via.Int64
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

// PurgeExpiredEnrollTokens drops tokens that can no longer be used.
func (s *Store) PurgeExpiredEnrollTokens(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM enroll_tokens WHERE used_at IS NOT NULL OR expires_at < ?`,
		time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		return fmt.Errorf("purge enroll tokens: %w", err)
	}
	return nil
}
