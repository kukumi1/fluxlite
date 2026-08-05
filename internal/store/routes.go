package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

// CreateRoute persists a route and its hops atomically. Hops must already
// carry their allocated relay ports.
func (s *Store) CreateRoute(ctx context.Context, r *model.Route) error {
	if err := r.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	r.CreatedAt, r.UpdatedAt = now, now

	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO routes (name, slug, target, protocol, enabled, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?)`,
			r.Name, r.Slug, r.Target, r.Protocol, r.Enabled, r.CreatedAt, r.UpdatedAt)
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("route name %q %w", r.Name, ErrConflict)
			}
			return fmt.Errorf("insert route: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("route id: %w", err)
		}
		r.ID = id
		for i := range r.Hops {
			r.Hops[i].RouteID = id
		}
		return insertHops(ctx, tx, r.Hops)
	})
}

func insertHops(ctx context.Context, tx *sql.Tx, hops []model.RouteHop) error {
	for _, h := range hops {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO route_hops (route_id, hop_order, node_id, relay_port)
			VALUES (?,?,?,?)`, h.RouteID, h.HopOrder, h.NodeID, h.RelayPort)
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("port %d on node %d is already claimed: %w",
					h.RelayPort, h.NodeID, ErrConflict)
			}
			return fmt.Errorf("insert hop %d: %w", h.HopOrder, err)
		}
	}
	return nil
}

// RouteByID returns a route with its hops loaded in order.
func (s *Store) RouteByID(ctx context.Context, id int64) (*model.Route, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, target, protocol, enabled, created_at, updated_at
		FROM routes WHERE id = ?`, id)

	r, err := scanRoute(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("route %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("scan route: %w", err)
	}
	if err := s.loadHops(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

// RouteByName returns a route by its unique name.
func (s *Store) RouteByName(ctx context.Context, name string) (*model.Route, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, target, protocol, enabled, created_at, updated_at
		FROM routes WHERE name = ?`, name)

	r, err := scanRoute(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("route %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("scan route: %w", err)
	}
	if err := s.loadHops(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

// RouteBySlug returns a route by its internal identifier.
func (s *Store) RouteBySlug(ctx context.Context, slug string) (*model.Route, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, slug, target, protocol, enabled, created_at, updated_at
		FROM routes WHERE slug = ?`, slug)

	r, err := scanRoute(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("route %q: %w", slug, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("scan route: %w", err)
	}
	if err := s.loadHops(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

// ListRoutes returns every route with hops loaded.
func (s *Store) ListRoutes(ctx context.Context) ([]*model.Route, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, slug, target, protocol, enabled, created_at, updated_at
		FROM routes ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("query routes: %w", err)
	}
	defer rows.Close()

	var out []*model.Route
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, fmt.Errorf("scan route: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range out {
		if err := s.loadHops(ctx, r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// RoutesOnNode returns routes that traverse the given node.
func (s *Store) RoutesOnNode(ctx context.Context, nodeID int64) ([]*model.Route, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.name, r.slug, r.target, r.protocol, r.enabled, r.created_at, r.updated_at
		FROM routes r
		JOIN route_hops h ON h.route_id = r.id
		WHERE h.node_id = ?
		GROUP BY r.id
		ORDER BY r.name`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("query routes on node: %w", err)
	}
	defer rows.Close()

	var out []*model.Route
	for rows.Next() {
		r, err := scanRoute(rows)
		if err != nil {
			return nil, fmt.Errorf("scan route: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, r := range out {
		if err := s.loadHops(ctx, r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpdateRoute replaces a route's fields and hops atomically.
func (s *Store) UpdateRoute(ctx context.Context, r *model.Route) error {
	if err := r.Validate(); err != nil {
		return err
	}
	r.UpdatedAt = time.Now().UTC()

	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE routes SET name=?, target=?, protocol=?, enabled=?, updated_at=?
			WHERE id=?`,
			r.Name, r.Target, r.Protocol, r.Enabled, r.UpdatedAt, r.ID)
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("route name %q %w", r.Name, ErrConflict)
			}
			return fmt.Errorf("update route: %w", err)
		}
		if err := checkAffected(res, "route", r.ID); err != nil {
			return err
		}
		// Hops are replaced wholesale: computing a minimal diff would buy
		// nothing since the applier reconciles the node state anyway.
		if _, err := tx.ExecContext(ctx, `DELETE FROM route_hops WHERE route_id = ?`, r.ID); err != nil {
			return fmt.Errorf("clear hops: %w", err)
		}
		for i := range r.Hops {
			r.Hops[i].RouteID = r.ID
		}
		return insertHops(ctx, tx, r.Hops)
	})
}

// SetRouteEnabled flips a route's enabled flag.
func (s *Store) SetRouteEnabled(ctx context.Context, id int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE routes SET enabled = ?, updated_at = ? WHERE id = ?`,
		enabled, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("set route enabled: %w", err)
	}
	return checkAffected(res, "route", id)
}

// DeleteRoute removes a route; hops cascade.
func (s *Store) DeleteRoute(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM routes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete route: %w", err)
	}
	return checkAffected(res, "route", id)
}

func scanRoute(row interface{ Scan(...any) error }) (*model.Route, error) {
	var r model.Route
	if err := row.Scan(&r.ID, &r.Name, &r.Slug, &r.Target, &r.Protocol, &r.Enabled,
		&r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *Store) loadHops(ctx context.Context, r *model.Route) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT route_id, hop_order, node_id, relay_port, latency_ms, latency_at
		FROM route_hops WHERE route_id = ? ORDER BY hop_order`, r.ID)
	if err != nil {
		return fmt.Errorf("query hops: %w", err)
	}
	defer rows.Close()

	r.Hops = nil
	for rows.Next() {
		var h model.RouteHop
		var latency sql.NullInt64
		var measured sql.NullTime
		if err := rows.Scan(&h.RouteID, &h.HopOrder, &h.NodeID, &h.RelayPort,
			&latency, &measured); err != nil {
			return fmt.Errorf("scan hop: %w", err)
		}
		if latency.Valid {
			ms := int(latency.Int64)
			h.LatencyMS = &ms
		}
		if measured.Valid {
			at := measured.Time
			h.LatencyAt = &at
		}
		r.Hops = append(r.Hops, h)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(r.Hops) > 0 {
		r.EntryPort = r.Hops[0].RelayPort
	}
	return nil
}

// SetHopLatencies records the measurement a verification produced for each
// hop. A hop whose latency could not be measured is left untouched rather than
// cleared, so one probe that hit a node without a usable timer does not erase
// an earlier good reading.
func (s *Store) SetHopLatencies(ctx context.Context, routeID int64, latencies map[int]int) error {
	if len(latencies) == 0 {
		return nil
	}
	now := time.Now().UTC()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for hopOrder, ms := range latencies {
			if _, err := tx.ExecContext(ctx, `
				UPDATE route_hops SET latency_ms = ?, latency_at = ?
				WHERE route_id = ? AND hop_order = ?`, ms, now, routeID, hopOrder); err != nil {
				return fmt.Errorf("record hop %d latency: %w", hopOrder, err)
			}
		}
		return nil
	})
}
