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

// quotaNearRatio is how full a route has to be before the panel starts looking
// more often. Enforcement can only act on what the last poll saw, so a route
// about to cross its limit is worth checking sooner than one sitting idle.
const quotaNearRatio = 0.9

// QuotaPeriodStart returns the first day of the billing period that contains
// now, as a YYYY-MM-DD string in the accounting timezone.
//
// The window is derived rather than stored. A stored period start is one more
// piece of state that can fall out of step with the calendar — after a clock
// change, a restart during rollover, or an edit to the reset day.
func QuotaPeriodStart(now time.Time, resetDay int) string {
	if resetDay < 1 {
		resetDay = 1
	}
	if resetDay > 28 {
		resetDay = 28
	}
	local := now.In(trafficZone)
	start := time.Date(local.Year(), local.Month(), resetDay, 0, 0, 0, 0, trafficZone)
	if local.Before(start) {
		start = start.AddDate(0, -1, 0)
	}
	return start.Format("2006-01-02")
}

// QuotaState is what the panel knows about one route's allowance.
type QuotaState struct {
	RouteID     int64  `json:"route_id"`
	PeriodStart string `json:"period_start"`
	UsedBytes   int64  `json:"used_bytes"`

	// Measured is false when no counter fed this period. The usage figure is
	// then meaningless and no quota is enforced against it.
	Measured bool `json:"measured"`
}

// EnforceQuotas stops routes that have used up their allowance and restarts
// the ones whose period has rolled over.
//
// It reports whether any capped route is close to its limit, which the caller
// uses to decide how soon to look again.
//
// A route whose traffic was never measured is left alone in both directions.
// Stopping it would cut a working chain on no evidence; treating the silence
// as zero would let it run past any allowance forever. Neither is acceptable,
// so the quota simply does not apply until counting works.
func (s *Service) EnforceQuotas(ctx context.Context) (nearLimit bool, err error) {
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return false, err
	}

	now := time.Now()
	var firstErr error
	for _, route := range routes {
		if route.QuotaBytes == nil {
			continue
		}
		quota := *route.QuotaBytes
		used, measured, uerr := s.store.PeriodUsage(ctx, route.ID,
			QuotaPeriodStart(now, route.QuotaResetDay))
		if uerr != nil {
			if firstErr == nil {
				firstErr = uerr
			}
			continue
		}
		if !measured {
			continue
		}

		switch {
		case used >= quota && route.Enabled:
			s.log.Warn("route reached its traffic quota, stopping it",
				"route", route.Name, "used", used, "quota", quota)
			if serr := s.pauseForQuota(ctx, route); serr != nil && firstErr == nil {
				firstErr = serr
			}

		// Usage back under the cap means the period rolled over, or the cap was
		// raised. Either way the reason the panel stopped it no longer holds.
		// Only routes the panel stopped are resumed: one an operator stopped
		// must stay stopped.
		case used < quota && route.QuotaPausedAt != nil:
			s.log.Info("traffic quota period rolled over, resuming route",
				"route", route.Name, "used", used, "quota", quota)
			if serr := s.resumeFromQuota(ctx, route.ID); serr != nil && firstErr == nil {
				firstErr = serr
			}
		}

		if float64(used) >= float64(quota)*quotaNearRatio {
			nearLimit = true
		}
	}
	return nearLimit, firstErr
}

// pauseForQuota disables the route and tears down whatever relays it can reach.
//
// The database flag is set even when a node cannot be reached, because that
// flag is what stops reconciliation from putting the route straight back. A
// relay left running on an unreachable node keeps carrying traffic, so it is
// reported rather than passed over.
func (s *Service) pauseForQuota(ctx context.Context, route *model.Route) error {
	now := time.Now().UTC()
	if err := s.store.SetRouteEnabled(ctx, route.ID, false); err != nil {
		return err
	}
	if err := s.store.SetQuotaPaused(ctx, route.ID, &now); err != nil {
		return err
	}
	if err := s.store.ClearHopRunning(ctx, route.ID); err != nil {
		return err
	}

	for _, hop := range route.Hops {
		node, err := s.store.NodeByID(ctx, hop.NodeID)
		if err != nil {
			continue
		}
		if rerr := s.applier.Remove(ctx, node, route.Slug); rerr != nil {
			s.log.Error("could not stop a hop of a quota-exceeded route; it is still forwarding",
				"route", route.Name, "node", node.Name, "error", rerr)
		}
	}
	return nil
}

// resumeFromQuota re-enables a route the panel had paused. Reconciliation
// redeploys it within one cycle, so nothing is applied here.
func (s *Service) resumeFromQuota(ctx context.Context, routeID int64) error {
	if err := s.store.SetRouteEnabled(ctx, routeID, true); err != nil {
		return err
	}
	return s.store.SetQuotaPaused(ctx, routeID, nil)
}

// QuotaStates reports period usage for every capped route.
func (s *Service) QuotaStates(ctx context.Context) ([]QuotaState, error) {
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []QuotaState
	for _, route := range routes {
		if route.QuotaBytes == nil {
			continue
		}
		start := QuotaPeriodStart(now, route.QuotaResetDay)
		used, measured, err := s.store.PeriodUsage(ctx, route.ID, start)
		if err != nil {
			return nil, err
		}
		out = append(out, QuotaState{
			RouteID: route.ID, PeriodStart: start, UsedBytes: used, Measured: measured,
		})
	}
	return out, nil
}
