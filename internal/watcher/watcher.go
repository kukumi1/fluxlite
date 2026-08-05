// Package watcher keeps deployed state converging on desired state.
//
// Nodes go offline, hosts reboot, and someone eventually edits a config by
// hand. A periodic reconcile catches all three without an agent running on
// the nodes.
package watcher

import (
	"context"
	"log/slog"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/store"
)

// Watcher runs background maintenance.
type Watcher struct {
	svc      *service.Service
	store    *store.Store
	log      *slog.Logger
	interval time.Duration
	latency  time.Duration
}

// Config configures the watcher.
type Config struct {
	Service *service.Service
	Store   *store.Store
	Logger  *slog.Logger

	// Interval governs reconciliation, which probes every node and re-applies
	// drifted routes.
	Interval time.Duration

	// LatencyInterval governs latency sampling. It is far shorter because a
	// sample is one TCP connect per hop over an already-open SSH connection,
	// where reconciliation runs a full probe including the UDP check.
	LatencyInterval time.Duration
}

func New(cfg Config) *Watcher {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	latency := cfg.LatencyInterval
	if latency <= 0 {
		latency = 30 * time.Second
	}
	return &Watcher{
		svc: cfg.Service, store: cfg.Store, log: cfg.Logger,
		interval: interval, latency: latency,
	}
}

// Run blocks until ctx is cancelled, reconciling on each tick.
func (w *Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	latencyTicker := time.NewTicker(w.latency)
	defer latencyTicker.Stop()

	w.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.reconcile(ctx)
		case <-latencyTicker.C:
			w.sampleLatencies(ctx)
		}
	}
}

// sampleLatencies refreshes the per-hop timings shown on the route list.
//
// A sample that outlives its interval is dropped rather than queued: the next
// tick produces a fresher number than a backlog ever could, and letting rounds
// pile up would multiply the load on the very nodes that are already slow.
func (w *Watcher) sampleLatencies(ctx context.Context) {
	routes, err := w.store.ListRoutes(ctx)
	if err != nil {
		w.log.Error("list routes for latency sampling", "error", err)
		return
	}

	deadline, cancel := context.WithTimeout(ctx, w.latency)
	defer cancel()

	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		select {
		case <-deadline.Done():
			return
		default:
		}
		if err := w.svc.MeasureRouteLatencies(deadline, route.ID); err != nil {
			// An unreachable node is ordinary here and already visible as the
			// node's status, so this stays at debug rather than crying wolf
			// every half minute.
			w.log.Debug("latency sample failed", "route", route.Name, "error", err)
		}
	}
}

func (w *Watcher) reconcile(ctx context.Context) {
	if err := w.store.PurgeExpiredSessions(ctx); err != nil {
		w.log.Error("purge expired sessions", "error", err)
	}
	if err := w.store.PurgeExpiredEnrollTokens(ctx); err != nil {
		w.log.Error("purge expired enroll tokens", "error", err)
	}
	w.refreshNodeStatus(ctx)
	w.reconcileRoutes(ctx)
}

// refreshNodeStatus records which nodes are currently reachable.
func (w *Watcher) refreshNodeStatus(ctx context.Context) {
	nodes, err := w.store.ListNodes(ctx)
	if err != nil {
		w.log.Error("list nodes", "error", err)
		return
	}

	for _, node := range nodes {
		select {
		case <-ctx.Done():
			return
		default:
		}

		probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		_, err := w.svc.ProbeNode(probeCtx, node.ID)
		cancel()

		if err != nil {
			w.log.Warn("node probe failed", "node", node.Name, "error", err)
			now := time.Now().UTC()
			if serr := w.store.SetNodeStatus(ctx, node.ID, model.StatusOffline, &now); serr != nil {
				w.log.Error("set node offline", "node", node.Name, "error", serr)
			}
		}
	}
}

// reconcileRoutes re-applies enabled routes whose relays are not running.
// Apply is idempotent: an unchanged config on a healthy service is a no-op,
// so this only acts where something actually drifted.
func (w *Watcher) reconcileRoutes(ctx context.Context) {
	statuses, err := w.svc.RouteStatuses(ctx)
	if err != nil {
		w.log.Error("read route statuses", "error", err)
		return
	}

	for _, status := range statuses {
		select {
		case <-ctx.Done():
			return
		default:
		}

		route, err := w.store.RouteByID(ctx, status.RouteID)
		if err != nil {
			w.log.Error("load route", "route", status.Name, "error", err)
			continue
		}
		if !route.Enabled {
			continue
		}

		needsApply := false
		for _, hop := range status.Hops {
			if !hop.Running {
				needsApply = true
				w.log.Warn("relay not running, scheduling reconcile",
					"route", status.Name, "node", hop.NodeName, "hop", hop.HopOrder, "error", hop.Error)
			}
		}
		if !needsApply {
			continue
		}

		applyCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		result, err := w.svc.ApplyRoute(applyCtx, route.ID)
		cancel()

		if err != nil {
			w.log.Error("reconcile route", "route", route.Name, "error", err)
			continue
		}
		if result.Failed() {
			for _, hop := range result.Hops {
				if hop.Error != "" {
					w.log.Error("reconcile hop failed",
						"route", route.Name, "node", hop.NodeName, "error", hop.Error)
				}
			}
			continue
		}
		w.log.Info("route reconciled", "route", route.Name)
	}
}
