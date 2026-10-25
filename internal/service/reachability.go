package service

import (
	"context"
	"sync"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

// offlineAfterMisses is how many metric samples in a row must fail to connect
// before a node is shown offline. At the default ten-second sample interval
// that is about half a minute: long enough to ride out one dropped packet or a
// redial, short enough that a machine that has gone away does not keep looking
// healthy until the next five-minute reconcile.
const offlineAfterMisses = 3

// reachability counts consecutive failed connections per node. It is kept in
// memory on purpose: after a restart every node starts from a clean slate and
// the first samples settle it within seconds.
type reachability struct {
	mu     sync.Mutex
	misses map[int64]int
}

// noteUnreachable records a failed connection and marks the node offline once
// the failures add up. The last-seen time is left alone so it keeps saying
// when the node last answered.
func (s *Service) noteUnreachable(ctx context.Context, node *model.Node) {
	s.reach.mu.Lock()
	if s.reach.misses == nil {
		s.reach.misses = make(map[int64]int)
	}
	s.reach.misses[node.ID]++
	misses := s.reach.misses[node.ID]
	s.reach.mu.Unlock()

	if misses < offlineAfterMisses || node.Status == model.StatusOffline {
		return
	}
	if err := s.store.SetNodeStatus(ctx, node.ID, model.StatusOffline, node.LastSeen); err != nil {
		s.log.Error("mark node offline", "node", node.Name, "error", err)
		return
	}
	s.log.Warn("node stopped answering, marked offline", "node", node.Name, "misses", misses)
}

// noteReachable clears the failure count and brings an offline node back as
// soon as it answers, rather than at the next reconcile.
func (s *Service) noteReachable(ctx context.Context, node *model.Node) {
	s.reach.mu.Lock()
	delete(s.reach.misses, node.ID)
	s.reach.mu.Unlock()

	if node.Status != model.StatusOffline {
		return
	}
	now := time.Now().UTC()
	if err := s.store.SetNodeStatus(ctx, node.ID, model.StatusOnline, &now); err != nil {
		s.log.Error("mark node online", "node", node.Name, "error", err)
		return
	}
	s.log.Info("node answering again, marked online", "node", node.Name)
}
