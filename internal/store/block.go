package store

import (
	"context"
	"fmt"
)

// BlockCheck is a machine's latest block check over one address family.
// Targets is the per-test-point detail as JSON.
type BlockCheck struct {
	NodeID    int64  `json:"node_id"`
	Family    int    `json:"family"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Note      string `json:"note"`
	Targets   string `json:"-"`
	CheckedAt int64  `json:"checked_at"`
}

func (s *Store) BlockChecks(ctx context.Context) ([]BlockCheck, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT node_id, family, kind, label, note, targets, checked_at
		FROM block_checks ORDER BY node_id, family`)
	if err != nil {
		return nil, fmt.Errorf("query block checks: %w", err)
	}
	defer rows.Close()
	var out []BlockCheck
	for rows.Next() {
		var b BlockCheck
		if err := rows.Scan(&b.NodeID, &b.Family, &b.Kind, &b.Label, &b.Note, &b.Targets, &b.CheckedAt); err != nil {
			return nil, fmt.Errorf("scan block check: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ReplaceBlockChecks swaps a machine's results for a new run's, so a family
// the machine no longer has does not linger.
func (s *Store) ReplaceBlockChecks(ctx context.Context, nodeID int64, checks []BlockCheck) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin block checks: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM block_checks WHERE node_id = ?`, nodeID); err != nil {
		return fmt.Errorf("clear block checks: %w", err)
	}
	for _, b := range checks {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO block_checks (node_id, family, kind, label, note, targets, checked_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			nodeID, b.Family, b.Kind, b.Label, b.Note, b.Targets, b.CheckedAt); err != nil {
			return fmt.Errorf("record block check: %w", err)
		}
	}
	return tx.Commit()
}
