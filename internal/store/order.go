package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var ErrInvalidOrder = errors.New("order must contain unique positive IDs")

type orderTable uint8

const (
	nodeOrder orderTable = iota
	routeOrder
)

// ReorderNodes saves the complete node order without touching node configuration.
func (s *Store) ReorderNodes(ctx context.Context, ids []int64) error {
	return s.reorder(ctx, nodeOrder, ids)
}

// ReorderRoutes saves display order, never the forwarding hops within a route.
func (s *Store) ReorderRoutes(ctx context.Context, ids []int64) error {
	return s.reorder(ctx, routeOrder, ids)
}

func (s *Store) reorder(ctx context.Context, kind orderTable, ids []int64) error {
	var table string
	switch kind {
	case nodeOrder:
		table = "nodes"
	case routeOrder:
		table = "routes"
	default:
		return errors.New("unsupported order table")
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return ErrInvalidOrder
		}
		seen[id] = true
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM "+table)
		if err != nil {
			return fmt.Errorf("read order membership: %w", err)
		}
		count, matches := 0, true
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			count++
			matches = matches && seen[id]
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if !matches || count != len(ids) {
			return fmt.Errorf("list changed; refresh before reordering: %w", ErrConflict)
		}
		for position, id := range ids {
			if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET sort_order=? WHERE id=?", position, id); err != nil {
				return fmt.Errorf("save order: %w", err)
			}
		}
		return nil
	})
}
