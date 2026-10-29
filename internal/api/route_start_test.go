package api

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/applier"
	"github.com/kukumi1/fluxlite/internal/model"
)

func tgStartFixture(t *testing.T, enabled bool) (*Server, *model.Route) {
	t.Helper()
	s := telegramPollFixture(t)
	n := tgTestNode(t, s)
	r := &model.Route{Name: "start", Slug: "start", EntryPort: 12000, Target: "example.com:443", Protocol: model.Protocol("tcp"), Enabled: enabled, Hops: []model.RouteHop{{NodeID: n.ID, RelayPort: 12000, HopOrder: 0}}}
	if e := s.svc.Store().CreateRoute(context.Background(), r); e != nil {
		t.Fatal(e)
	}
	return s, r
}
func tgSuccessfulApply() *applier.Result {
	return &applier.Result{Hops: []applier.HopOutcome{{HopOrder: 0, Action: "applied"}}}
}
func TestTelegramStartRouteEnablesOnlyAfterSuccessfulDeployment(t *testing.T) {
	s, r := tgStartFixture(t, false)
	calls := 0
	_, err := s.startRoute(context.Background(), r.ID, func(ctx context.Context, id int64) (*applier.Result, error) {
		calls++
		current, e := s.svc.Store().RouteByID(ctx, id)
		if e != nil || current.Enabled {
			t.Fatal("draft enabled before deployment finished")
		}
		return tgSuccessfulApply(), nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("start: %v calls %d", err, calls)
	}
	current, e := s.svc.Store().RouteByID(context.Background(), r.ID)
	if e != nil || !current.Enabled {
		t.Fatal("successful deployment not enabled")
	}
}
func TestTelegramStartRouteFailurePreservesPreviousEnabledFlag(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, kind := range []string{"partial", "error", "nil", "missing", "skipped", "cancel"} {
			t.Run(kind+map[bool]string{false: "-disabled", true: "-enabled"}[enabled], func(t *testing.T) {
				s, r := tgStartFixture(t, enabled)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				_, err := s.startRoute(ctx, r.ID, func(context.Context, int64) (*applier.Result, error) {
					switch kind {
					case "partial":
						return &applier.Result{Hops: []applier.HopOutcome{{Error: "private ssh error", Action: "unreachable"}}}, nil
					case "error":
						return nil, errors.New("private ssh error")
					case "nil":
						return nil, nil
					case "missing":
						return &applier.Result{}, nil
					case "skipped":
						return &applier.Result{Hops: []applier.HopOutcome{{Action: "skipped"}}}, nil
					case "cancel":
						cancel()
						return tgSuccessfulApply(), nil
					}
					panic("unreachable")
				})
				if err == nil {
					t.Fatal("failed deployment accepted")
				}
				current, e := s.svc.Store().RouteByID(context.Background(), r.ID)
				if e != nil || current.Enabled != enabled {
					t.Fatalf("enabled flag changed: %+v %v", current, e)
				}
			})
		}
	}
}
func TestTelegramStartRouteCancellationRollbackUsesDetachedContext(t *testing.T) {
	s, r := tgStartFixture(t, false)
	if e := s.svc.Store().SetRouteEnabled(context.Background(), r.ID, true); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := s.enableStartedRoute(ctx, r); e == nil {
		t.Fatal("canceled write succeeded")
	}
	current, e := s.svc.Store().RouteByID(context.Background(), r.ID)
	if e != nil || current.Enabled {
		t.Fatal("rollback reused canceled context")
	}
}
func TestTelegramStartRouteRejectsQuotaAndNewPause(t *testing.T) {
	for _, kind := range []string{"paused", "zero", "exhausted", "during-apply"} {
		t.Run(kind, func(t *testing.T) {
			s, r := tgStartFixture(t, false)
			ctx := context.Background()
			switch kind {
			case "paused":
				now := time.Now()
				if e := s.svc.Store().SetQuotaPaused(ctx, r.ID, &now); e != nil {
					t.Fatal(e)
				}
			case "zero":
				if _, e := s.svc.Store().DB().Exec(`UPDATE routes SET quota_bytes=0 WHERE id=?`, r.ID); e != nil {
					t.Fatal(e)
				}
			case "exhausted":
				if _, e := s.svc.Store().DB().Exec(`UPDATE routes SET quota_bytes=1 WHERE id=?`, r.ID); e != nil {
					t.Fatal(e)
				}
				if e := s.svc.Store().RecordTraffic(ctx, r.ID, 0, 100, 0, time.Now().Format("2006-01-02"), true); e != nil {
					t.Fatal(e)
				}
			}
			if kind == "exhausted" {
				if e := s.svc.Store().RecordTraffic(ctx, r.ID, 0, 200, 0, time.Now().Format("2006-01-02"), true); e != nil {
					t.Fatal(e)
				}
			}
			calls := 0
			_, err := s.startRoute(ctx, r.ID, func(context.Context, int64) (*applier.Result, error) {
				calls++
				if kind == "during-apply" {
					now := time.Now()
					if e := s.svc.Store().SetQuotaPaused(ctx, r.ID, &now); e != nil {
						t.Fatal(e)
					}
				}
				return tgSuccessfulApply(), nil
			})
			if !errors.Is(err, errRouteQuota) {
				t.Fatalf("expected quota error: %v", err)
			}
			if kind != "during-apply" && calls != 0 {
				t.Fatal("exhausted route deployed")
			}
			current, e := s.svc.Store().RouteByID(ctx, r.ID)
			if e != nil || current.Enabled {
				t.Fatal("quota route enabled")
			}
		})
	}
}
func TestTelegramPartialDeploymentAuditsFailure(t *testing.T) {
	s, r := tgStartFixture(t, false)
	ctx := context.Background()
	scope, e := s.newTelegramScope(ctx, 42)
	if e != nil {
		t.Fatal(e)
	}
	identity, _, e := s.telegramResource(ctx, "routes", r.ID)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.telegramExecuteWithApply(ctx, telegramBotUser{ID: 42}, telegramConfirmation{Scope: scope, Action: "routes:apply", ID: r.ID, Identity: identity}, func(context.Context, int64) (*applier.Result, error) {
		return &applier.Result{Hops: []applier.HopOutcome{{Error: "private SSH stderr", Action: "unreachable"}}}, nil
	})
	if !errors.Is(e, errRouteDeploy) {
		t.Fatal("partial apply did not fail")
	}
	var detail string
	if e = s.svc.Store().DB().QueryRow(`SELECT detail FROM audit_logs WHERE action='telegram.routes:apply'`).Scan(&detail); e != nil || detail != "failed" {
		t.Fatalf("bad audit %q: %v", detail, e)
	}
}

func TestStoppedRouteMustBeStartedNotApplied(t *testing.T) {
	srv, st, _, cookie := recoveryServer(t)
	ctx := context.Background()
	n := &model.Node{Name: "relay", Host: "192.0.2.10", SSHPort: 22, SSHUser: "root", AuthType: model.AuthPassword, AuthSecret: []byte("test"), PortStart: 10000, PortEnd: 11000}
	if e := st.CreateNode(ctx, n); e != nil {
		t.Fatal(e)
	}
	r := &model.Route{Name: "stopped", Slug: "stopped", EntryPort: 10001, Target: "example.com:443", Protocol: model.Protocol("tcp"), Hops: []model.RouteHop{{NodeID: n.ID, RelayPort: 10001, HopOrder: 0}}}
	if e := st.CreateRoute(ctx, r); e != nil {
		t.Fatal(e)
	}
	h := srv.Handler()

	w := orderHTTP(t, h, "POST", fmt.Sprintf("/api/routes/%d/apply", r.ID), cookie, "192.0.2.3", nil)
	if w.Code != 409 {
		t.Fatalf("apply on a stopped route: %d %s", w.Code, w.Body)
	}

	now := time.Now()
	if e := st.SetQuotaPaused(ctx, r.ID, &now); e != nil {
		t.Fatal(e)
	}
	w = orderHTTP(t, h, "POST", fmt.Sprintf("/api/routes/%d/start", r.ID), cookie, "192.0.2.3", nil)
	if w.Code != 409 {
		t.Fatalf("start past quota: %d %s", w.Code, w.Body)
	}
	if got, e := st.RouteByID(ctx, r.ID); e != nil || got.Enabled {
		t.Fatal("route enabled past its quota", e)
	}
}
