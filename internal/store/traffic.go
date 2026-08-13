package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

// RecordTraffic folds one kernel counter reading into a hop's running total.
//
// The kernel counters are absolute and volatile: they start at zero when the
// rule is created and go back to zero on reboot or a firewall flush. Only the
// growth between two readings is real traffic, so the previous reading is kept
// and subtracted.
//
// A reading lower than the one before it means the counter restarted. The
// whole of the new reading is then the traffic since that restart — treating it
// as a negative delta would silently eat the total, and skipping it would lose
// everything accumulated until the next poll.
//
// The first reading of a rule establishes a baseline and contributes nothing:
// with no previous value there is no way to tell traffic this panel is
// responsible for from traffic that flowed before it started watching.
func (s *Store) RecordTraffic(ctx context.Context, routeID int64, hopOrder int, rawIn, rawOut uint64, day string, isEntry bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin traffic tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var prevIn, prevOut sql.NullInt64
	err = tx.QueryRowContext(ctx,
		`SELECT raw_in, raw_out FROM route_traffic WHERE route_id = ? AND hop_order = ?`,
		routeID, hopOrder).Scan(&prevIn, &prevOut)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return fmt.Errorf("read traffic baseline: %w", err)
	}

	deltaIn := counterDelta(prevIn, rawIn)
	deltaOut := counterDelta(prevOut, rawOut)

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO route_traffic (route_id, hop_order, bytes_in, bytes_out, raw_in, raw_out, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(route_id, hop_order) DO UPDATE SET
			bytes_in   = bytes_in  + excluded.bytes_in,
			bytes_out  = bytes_out + excluded.bytes_out,
			raw_in     = excluded.raw_in,
			raw_out    = excluded.raw_out,
			updated_at = excluded.updated_at`,
		routeID, hopOrder, deltaIn, deltaOut, int64(rawIn), int64(rawOut), time.Now().UTC()); err != nil {
		return fmt.Errorf("record hop %d traffic: %w", hopOrder, err)
	}

	// Only the entry hop feeds the daily buckets. Every byte the route carries
	// passes through every hop, so adding them all up would multiply the total
	// by the length of the chain.
	if isEntry && (deltaIn > 0 || deltaOut > 0) {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO route_traffic_daily (route_id, day, bytes_in, bytes_out)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(route_id, day) DO UPDATE SET
				bytes_in  = bytes_in  + excluded.bytes_in,
				bytes_out = bytes_out + excluded.bytes_out`,
			routeID, day, deltaIn, deltaOut); err != nil {
			return fmt.Errorf("record daily traffic: %w", err)
		}
	}

	return tx.Commit()
}

// counterDelta returns how much a kernel counter grew since the last reading.
func counterDelta(prev sql.NullInt64, now uint64) int64 {
	if !prev.Valid {
		return 0
	}
	if now < uint64(prev.Int64) {
		return int64(now)
	}
	return int64(now - uint64(prev.Int64))
}

// ResetTrafficBaseline forgets the raw readings for a route without touching
// its totals, so a rebuilt counter rule starts a fresh baseline rather than
// having its from-zero reading counted as a sudden burst.
func (s *Store) ResetTrafficBaseline(ctx context.Context, routeID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE route_traffic SET raw_in = NULL, raw_out = NULL WHERE route_id = ?`, routeID)
	if err != nil {
		return fmt.Errorf("reset traffic baseline: %w", err)
	}
	return nil
}

// RouteTraffic reports a route's cumulative totals keyed by route id.
//
// The figure wanted is the entry hop's, which is the only hop every byte must
// cross. When the entry hop is not counting — its node may have no iptables —
// the lowest hop that is counting stands in, and FromEntry says so. Passing a
// middle hop's bytes off as the route's total would understate any traffic the
// chain drops before reaching it, with nothing on screen to suggest doubt.
//
// Routes absent from the map have never been sampled; that is not zero traffic.
func (s *Store) RouteTraffic(ctx context.Context) (map[int64]*model.Traffic, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.route_id, t.hop_order, t.bytes_in, t.bytes_out, t.updated_at,
		       (SELECT MIN(hop_order) FROM route_hops WHERE route_id = t.route_id)
		FROM route_traffic t
		WHERE t.hop_order = (SELECT MIN(hop_order) FROM route_traffic WHERE route_id = t.route_id)`)
	if err != nil {
		return nil, fmt.Errorf("list route traffic: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]*model.Traffic)
	for rows.Next() {
		var id int64
		var entryHop sql.NullInt64
		var t model.Traffic
		if err := rows.Scan(&id, &t.HopOrder, &t.BytesIn, &t.BytesOut, &t.UpdatedAt, &entryHop); err != nil {
			return nil, fmt.Errorf("scan route traffic: %w", err)
		}
		t.FromEntry = entryHop.Valid && int(entryHop.Int64) == t.HopOrder
		out[id] = &t
	}
	return out, rows.Err()
}

// DailyTraffic returns a route's per-day totals, most recent first.
func (s *Store) DailyTraffic(ctx context.Context, routeID int64, days int) ([]model.DailyTraffic, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT day, bytes_in, bytes_out FROM route_traffic_daily
		WHERE route_id = ? ORDER BY day DESC LIMIT ?`, routeID, days)
	if err != nil {
		return nil, fmt.Errorf("list daily traffic: %w", err)
	}
	defer rows.Close()

	var out []model.DailyTraffic
	for rows.Next() {
		var d model.DailyTraffic
		if err := rows.Scan(&d.Day, &d.BytesIn, &d.BytesOut); err != nil {
			return nil, fmt.Errorf("scan daily traffic: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
