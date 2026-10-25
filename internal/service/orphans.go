package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/store"
)

// orphanScanTimeout bounds the scan and cleanup of one node.
const orphanScanTimeout = 30 * time.Second

// RemoveOrphanRelays tears down relays that no route in the panel accounts for.
//
// They come from teardowns that could not finish: a route deleted while one of
// its machines was unreachable, or a hop dropped from a chain while that
// machine was down. Left alone such a relay keeps its port and keeps
// forwarding traffic the panel no longer shows.
//
// A relay is an orphan on a node when its route is gone, or when the route
// still exists but no longer passes through that node. Each slug is looked up
// at the moment it is judged, never from a list taken earlier, so a route
// created while the scan is running cannot be mistaken for one that was
// deleted.
func (s *Service) RemoveOrphanRelays(ctx context.Context) error {
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return err
	}
	// With no routes at all, every relay on every machine would be judged an
	// orphan. That is far more likely a panel started on an empty or wrong
	// database than a fleet whose routes were all deleted, and tearing down
	// the whole fleet on that guess cannot be undone.
	if len(routes) == 0 {
		return nil
	}

	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, node := range nodes {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if node.Status != model.StatusOnline {
			continue
		}
		nodeCtx, cancel := context.WithTimeout(ctx, orphanScanTimeout)
		err := s.removeOrphansOn(nodeCtx, node)
		cancel()
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Service) removeOrphansOn(ctx context.Context, node *model.Node) error {
	deployed, err := s.applier.Deployed(ctx, node)
	if err != nil {
		return err
	}
	for _, slug := range deployed {
		orphan, err := s.isOrphanOn(ctx, slug, node.ID)
		if err != nil {
			return err
		}
		if !orphan {
			continue
		}

		detail := fmt.Sprintf("已清理不属于任何链路的转发实例 %s", slug)
		if rerr := s.applier.Remove(ctx, node, slug); rerr != nil {
			detail = fmt.Sprintf("发现不属于任何链路的转发实例 %s，清理失败：%v", slug, rerr)
			s.log.Warn("orphan relay cleanup failed", "node", node.Name, "slug", slug, "error", rerr)
		} else {
			s.log.Warn("removed orphan relay", "node", node.Name, "slug", slug)
		}
		entry := &model.AuditLog{Actor: "system", Action: "route.orphan_removed", Target: node.Name, Detail: detail}
		if aerr := s.store.AppendAudit(ctx, entry); aerr != nil {
			s.log.Error("audit orphan relay cleanup", "node", node.Name, "slug", slug, "error", aerr)
		}
	}
	return nil
}

func (s *Service) isOrphanOn(ctx context.Context, slug string, nodeID int64) (bool, error) {
	route, err := s.store.RouteBySlug(ctx, slug)
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	for _, hop := range route.Hops {
		if hop.NodeID == nodeID {
			return false, nil
		}
	}
	return true, nil
}
