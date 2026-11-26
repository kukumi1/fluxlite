package store

import (
	"context"
	"database/sql"
	"fmt"
)

// LatencySample is one round of probes from a machine to one test point,
// over IPv4 or IPv6 (Family 4 or 6). AvgMS is nil when no probe connected,
// or when the machine could connect but had nothing to time it with.
type LatencySample struct {
	NodeID int64  `json:"node_id"`
	Target string `json:"target"`
	Family int    `json:"family"`
	At     int64  `json:"at"`
	AvgMS  *int   `json:"avg_ms"`
	Loss   int    `json:"loss"`
}

func (s *Store) AddLatencySamples(ctx context.Context, samples []LatencySample) error {
	if len(samples) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, l := range samples {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO latency_samples (node_id, target, family, at, avg_ms, loss) VALUES (?, ?, ?, ?, ?, ?)`,
				l.NodeID, l.Target, familyOf(l), l.At, l.AvgMS, l.Loss); err != nil {
				return fmt.Errorf("record latency sample: %w", err)
			}
		}
		return nil
	})
}

func familyOf(l LatencySample) int {
	if l.Family == 6 {
		return 6
	}
	return 4
}

// LatestLatency returns each machine's most recent sample per test point and
// address family taken at or after since.
func (s *Store) LatestLatency(ctx context.Context, since int64) ([]LatencySample, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.node_id, l.target, l.family, l.at, l.avg_ms, l.loss FROM latency_samples l
		JOIN (SELECT node_id, target, family, MAX(at) AS at FROM latency_samples WHERE at >= ? GROUP BY node_id, target, family) m
		  ON m.node_id = l.node_id AND m.target = l.target AND m.family = l.family AND m.at = l.at`, since)
	if err != nil {
		return nil, fmt.Errorf("query latest latency: %w", err)
	}
	return scanLatency(rows)
}

// LatencyHistory averages a machine's samples over one address family into
// buckets of the given width, so a week of three-minute samples comes back
// as a chartable series. A bucket where nothing connected has a nil average
// and full loss.
func (s *Store) LatencyHistory(ctx context.Context, nodeID int64, family int, since, bucket int64) ([]LatencySample, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT node_id, target, family, (at / ?) * ?, CAST(ROUND(AVG(avg_ms)) AS INTEGER), CAST(ROUND(AVG(loss)) AS INTEGER)
		FROM latency_samples WHERE node_id = ? AND family = ? AND at >= ?
		GROUP BY target, at / ? ORDER BY 4`, bucket, bucket, nodeID, family, since, bucket)
	if err != nil {
		return nil, fmt.Errorf("query latency history: %w", err)
	}
	return scanLatency(rows)
}

func scanLatency(rows *sql.Rows) ([]LatencySample, error) {
	defer rows.Close()
	var out []LatencySample
	for rows.Next() {
		var l LatencySample
		var avg sql.NullInt64
		if err := rows.Scan(&l.NodeID, &l.Target, &l.Family, &l.At, &avg, &l.Loss); err != nil {
			return nil, fmt.Errorf("scan latency sample: %w", err)
		}
		if avg.Valid {
			ms := int(avg.Int64)
			l.AvgMS = &ms
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) PruneLatency(ctx context.Context, before int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM latency_samples WHERE at < ?`, before); err != nil {
		return fmt.Errorf("prune latency samples: %w", err)
	}
	return nil
}

// ReturnRoute is the latest trace from a machine back to one test point.
// Verdict and Hops are JSON owned by the service; Line and Confidence are
// copied out of the verdict so a change of line can be found without it.
type ReturnRoute struct {
	NodeID     int64
	Target     string
	Line       string
	Confidence string
	Verdict    string
	Hops       string
	Error      string
	CheckedAt  int64
}

func (s *Store) ReturnRoutes(ctx context.Context) ([]ReturnRoute, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT node_id, target, line, confidence, verdict, hops, error, checked_at
		FROM return_routes ORDER BY node_id, target`)
	if err != nil {
		return nil, fmt.Errorf("query return routes: %w", err)
	}
	defer rows.Close()
	var out []ReturnRoute
	for rows.Next() {
		var r ReturnRoute
		if err := rows.Scan(&r.NodeID, &r.Target, &r.Line, &r.Confidence, &r.Verdict, &r.Hops, &r.Error, &r.CheckedAt); err != nil {
			return nil, fmt.Errorf("scan return route: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) SetReturnRoute(ctx context.Context, r ReturnRoute) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO return_routes (node_id, target, line, confidence, verdict, hops, error, checked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id, target) DO UPDATE SET
			line = excluded.line, confidence = excluded.confidence, verdict = excluded.verdict,
			hops = excluded.hops, error = excluded.error, checked_at = excluded.checked_at`,
		r.NodeID, r.Target, r.Line, r.Confidence, r.Verdict, r.Hops, r.Error, r.CheckedAt)
	if err != nil {
		return fmt.Errorf("record return route: %w", err)
	}
	return nil
}
