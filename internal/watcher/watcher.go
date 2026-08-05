// Package watcher keeps deployed state converging on desired state.
//
// Nodes go offline, hosts reboot, and someone eventually edits a config by
// hand. A periodic reconcile catches all three without an agent running on
// the nodes.
package watcher

import (
	"context"
	"log/slog"
	"sync"
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
	sample   time.Duration
}

// Config configures the watcher.
type Config struct {
	Service *service.Service
	Store   *store.Store
	Logger  *slog.Logger

	// Interval governs reconciliation, which probes every node and re-applies
	// drifted routes.
	Interval time.Duration

	// SampleInterval governs runtime sampling: liveness and latency per hop.
	// It is far shorter than Interval because a sample is two short commands
	// per hop over an already-open SSH connection, where reconciliation probes
	// every node and compares every config.
	SampleInterval time.Duration
}

func New(cfg Config) *Watcher {
	interval := cfg.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	sample := cfg.SampleInterval
	if sample <= 0 {
		sample = 30 * time.Second
	}
	return &Watcher{
		svc: cfg.Service, store: cfg.Store, log: cfg.Logger,
		interval: interval, sample: sample,
	}
}

// Run blocks until ctx is cancelled.
//
// Reconciliation and sampling run on separate goroutines because they operate
// on wildly different timescales: a reconcile probes every node, UDP check
// included, and takes minutes on a fleet of any size. Sharing one loop let it
// starve the sampler for that entire stretch, which is the opposite of what a
// thirty-second sample interval promises.
func (w *Watcher) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		w.loop(ctx, w.interval, w.reconcile, true)
	}()
	go func() {
		defer wg.Done()
		w.loop(ctx, w.sample, w.sampleRoutes, true)
	}()

	wg.Wait()
}

// loop runs fn on a ticker, optionally once up front.
func (w *Watcher) loop(ctx context.Context, every time.Duration, fn func(context.Context), immediate bool) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	if immediate {
		fn(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn(ctx)
		}
	}
}

// sampleRoutes refreshes the liveness and timings shown on the route list.
//
// A round that outlives its interval is dropped rather than queued: the next
// tick produces fresher facts than a backlog ever could, and letting rounds
// pile up would multiply the load on the very nodes that are already slow.
func (w *Watcher) sampleRoutes(ctx context.Context) {
	routes, err := w.store.ListRoutes(ctx)
	if err != nil {
		w.log.Error("list routes for sampling", "error", err)
		return
	}

	deadline, cancel := context.WithTimeout(ctx, w.sample)
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
		if err := w.svc.SampleRoute(deadline, route.ID); err != nil {
			// An unreachable node is ordinary here and already visible as the
			// node's status, so this stays at debug rather than crying wolf
			// every half minute.
			w.log.Debug("route sample failed", "route", route.Name, "error", err)
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

// reconcileRoutes re-applies every enabled route.
//
// Apply is idempotent by hash: a config that already matches on a running
// service is left completely alone, so this costs a comparison per hop and
// touches nothing that has not drifted.
//
// It deliberately does not gate on liveness. Gating there caught a relay that
// had died but never a config someone had edited by hand — the process keeps
// running happily on the edited file, and the drift would survive until the
// next manual deploy. Rewriting it restarts the relay, which is the point:
// what the node is running is no longer what the panel was asked for.
func (w *Watcher) reconcileRoutes(ctx context.Context) {
	routes, err := w.store.ListRoutes(ctx)
	if err != nil {
		w.log.Error("list routes for reconcile", "error", err)
		return
	}

	for _, route := range routes {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !route.Enabled {
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

		// Only a hop that actually changed is worth a line in the log; a quiet
		// reconcile of a healthy fleet should stay quiet.
		for _, hop := range result.Hops {
			if hop.Changed {
				w.log.Warn("node state had drifted, corrected",
					"route", route.Name, "node", hop.NodeName, "action", hop.Action)
			}
		}
	}
}
