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
	errTelegramRouteQuota  = errors.New("route is paused or its quota is exhausted")
	errTelegramRouteDeploy = errors.New("route deployment incomplete; remote services may remain")
)

type telegramApplyRoute func(context.Context, int64) (*applier.Result, error)

// Only the Bot's explicit deploy action enables a draft route. ApplyRoute's
// existing API semantics remain unchanged. A disabled draft stays disabled
// while remote work is in flight, so reconciliation cannot race that work.
func (s *Server) telegramStartRoute(ctx context.Context, id int64, apply telegramApplyRoute) (err error) {
	route, err := s.svc.Store().RouteByID(ctx, id)
	if err != nil {
		return err
	}
	if err = s.telegramCheckRouteQuota(ctx, route); err != nil {
		return err
	}
	result, err := apply(ctx, id)
	if err != nil {
		return errors.Join(errTelegramRouteDeploy, err)
	}
	if result == nil || result.Failed() || len(result.Hops) != len(route.Hops) {
		return errTelegramRouteDeploy
	}
	for _, hop := range result.Hops {
		if hop.Action == "skipped" || hop.Action == "unreachable" {
			return errTelegramRouteDeploy
		}
	}
	if err = ctx.Err(); err != nil {
		return errors.Join(errTelegramRouteDeploy, err)
	}
	current, err := s.svc.Store().RouteByID(ctx, id)
	if err != nil {
		return errors.Join(errTelegramRouteDeploy, err)
	}
	if err = s.telegramCheckRouteQuota(ctx, current); err != nil {
		return errors.Join(errTelegramRouteDeploy, err)
	}
	return s.telegramEnableStartedRoute(ctx, route)
}

func (s *Server) telegramEnableStartedRoute(ctx context.Context, route *model.Route) (err error) {
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
		return errors.Join(errTelegramRouteDeploy, err)
	}
	if err = ctx.Err(); err != nil {
		return errors.Join(errTelegramRouteDeploy, err)
	}
	return nil
}

func (s *Server) telegramCheckRouteQuota(ctx context.Context, route *model.Route) error {
	if route.QuotaPausedAt != nil {
		return errTelegramRouteQuota
	}
	if route.QuotaBytes == nil {
		return nil
	}
	if *route.QuotaBytes <= 0 {
		return errTelegramRouteQuota
	}
	used, measured, err := s.svc.Store().PeriodUsage(ctx, route.ID, service.QuotaPeriodStart(time.Now(), route.QuotaResetDay))
	if err != nil {
		return err
	}
	if measured && used >= *route.QuotaBytes {
		return errTelegramRouteQuota
	}
	return nil
}
