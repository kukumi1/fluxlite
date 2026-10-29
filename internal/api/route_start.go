package api

import (
	"context"
	"errors"
	"time"

	"github.com/kukumi1/fluxlite/internal/applier"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/service"
)

var (
	errRouteQuota  = errors.New("route is paused or its quota is exhausted")
	errRouteDeploy = errors.New("route deployment incomplete; remote services may remain")
)

type routeApplier func(context.Context, int64) (*applier.Result, error)

// startRoute deploys a stopped or draft route and enables it only once every
// hop is confirmed up. Plain ApplyRoute leaves the enabled flag alone, so a
// stopped route redeployed that way runs on its machines while the panel
// neither counts nor caps it. The route stays disabled while remote work is in
// flight, so reconciliation cannot race that work, and a route whose quota is
// spent is refused before anything is touched.
//
// The deploy result is returned whenever there is one, so a caller can say
// which hop failed.
func (s *Server) startRoute(ctx context.Context, id int64, apply routeApplier) (*applier.Result, error) {
	route, err := s.svc.Store().RouteByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = s.checkRouteQuota(ctx, route); err != nil {
		return nil, err
	}
	result, err := apply(ctx, id)
	if err != nil {
		return result, errors.Join(errRouteDeploy, err)
	}
	if result == nil || result.Failed() || len(result.Hops) != len(route.Hops) {
		return result, errRouteDeploy
	}
	for _, hop := range result.Hops {
		if hop.Action == "skipped" || hop.Action == "unreachable" {
			return result, errRouteDeploy
		}
	}
	if err = ctx.Err(); err != nil {
		return result, errors.Join(errRouteDeploy, err)
	}
	current, err := s.svc.Store().RouteByID(ctx, id)
	if err != nil {
		return result, errors.Join(errRouteDeploy, err)
	}
	if err = s.checkRouteQuota(ctx, current); err != nil {
		return result, errors.Join(errRouteDeploy, err)
	}
	return result, s.enableStartedRoute(ctx, route)
}

func (s *Server) enableStartedRoute(ctx context.Context, route *model.Route) (err error) {
	id := route.ID
	// A canceled write may have reached SQLite. Roll back with an independent,
	// bounded context rather than reusing the canceled request. Never undo a
	// quota pause that happened during deployment.
	defer func() {
		if err == nil {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		fresh, readErr := s.svc.Store().RouteByID(rollbackCtx, id)
		if readErr != nil {
			err = errors.Join(err, readErr)
			return
		}
		enabled := route.Enabled
		if fresh.QuotaPausedAt != nil {
			enabled = false
		}
		if rollbackErr := s.svc.Store().SetRouteEnabled(rollbackCtx, id, enabled); rollbackErr != nil {
			err = errors.Join(err, rollbackErr)
		}
	}()
	if err = s.svc.Store().SetRouteEnabled(ctx, id, true); err != nil {
		return errors.Join(errRouteDeploy, err)
	}
	if err = ctx.Err(); err != nil {
		return errors.Join(errRouteDeploy, err)
	}
	return nil
}

func (s *Server) checkRouteQuota(ctx context.Context, route *model.Route) error {
	if route.QuotaPausedAt != nil {
		return errRouteQuota
	}
	if route.QuotaBytes == nil {
		return nil
	}
	if *route.QuotaBytes <= 0 {
		return errRouteQuota
	}
	used, measured, err := s.svc.Store().PeriodUsage(ctx, route.ID, service.QuotaPeriodStart(time.Now(), route.QuotaResetDay))
	if err != nil {
		return err
	}
	if measured && used >= *route.QuotaBytes {
		return errRouteQuota
	}
	return nil
}
