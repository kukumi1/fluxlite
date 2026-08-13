package service

import (
	"context"
	"fmt"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

// trafficZone is the timezone daily buckets are cut in.
//
// It is deliberately not the panel host's local time: providers bill by their
// own calendar and the machine the panel happens to run on is often set to
// whatever its image shipped with, so "today" would silently mean a different
// day depending on where the panel was deployed.
var trafficZone = time.FixedZone("UTC+8", 8*3600)

// CollectTraffic reads every node's byte counters once and folds the growth
// into each route's totals.
//
// Counters are read per node rather than per route: one command returns the
// whole chain, so a machine carrying six routes costs the same as one carrying
// one. A node that cannot be reached is skipped entirely — its routes keep
// their last known totals rather than being recorded as having moved no bytes.
func (s *Service) CollectTraffic(ctx context.Context) error {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return err
	}

	type hopRef struct {
		routeID  int64
		hopOrder int
		isEntry  bool
	}
	// (slug, node) identifies exactly one hop: a route crosses a given machine
	// at most once, so the pair is enough to attribute a counter.
	index := make(map[string]map[int64]hopRef)
	for _, route := range routes {
		byNode := make(map[int64]hopRef, len(route.Hops))
		for _, hop := range route.Hops {
			byNode[hop.NodeID] = hopRef{
				routeID:  route.ID,
				hopOrder: hop.HopOrder,
				isEntry:  hop.HopOrder == entryHopOrder(route),
			}
		}
		index[route.Slug] = byNode
	}

	day := time.Now().In(trafficZone).Format("2006-01-02")

	var firstErr error
	for _, node := range nodes {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if node.Status == model.StatusOffline {
			continue
		}

		counters, err := s.applier.ReadCounters(ctx, node)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", node.Name, err)
			}
			continue
		}

		for slug, c := range counters {
			ref, ok := index[slug][node.ID]
			if !ok {
				// A counter for a route this node no longer carries. Removal
				// takes care of these; attributing them would be a guess.
				continue
			}
			if err := s.store.RecordTraffic(ctx, ref.routeID, ref.hopOrder, c.In, c.Out, day, ref.isEntry); err != nil {
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

// entryHopOrder returns the hop order of the route's first hop.
func entryHopOrder(route *model.Route) int {
	entry := 0
	for i, hop := range route.Hops {
		if i == 0 || hop.HopOrder < entry {
			entry = hop.HopOrder
		}
	}
	return entry
}

// RouteTraffic returns cumulative totals keyed by route id.
func (s *Service) RouteTraffic(ctx context.Context) (map[int64]*model.Traffic, error) {
	return s.store.RouteTraffic(ctx)
}

// DailyTraffic returns a route's recent per-day totals, newest first.
func (s *Service) DailyTraffic(ctx context.Context, routeID int64, days int) ([]model.DailyTraffic, error) {
	if days <= 0 || days > 365 {
		days = 30
	}
	return s.store.DailyTraffic(ctx, routeID, days)
}
