package service

import (
	"context"
	"errors"
	"github.com/kukumi1/fluxlite/internal/model"
	"sync"
	"testing"
	"time"
)

func TestSingBoxTrafficIsolatesExpiredMemberFailure(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "failure"
		if timeout {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			s, n := subscriptionTestService(t)
			ctx := context.Background()
			bad := collectionUser(t, s, n.ID, 10001)
			quota := collectionUser(t, s, n.ID, 10002)
			expired := collectionUser(t, s, n.ID, 10003)
			_, _, err := s.CreateSubscription(ctx, SubscriptionInput{Name: "collection", UserIDs: []int64{bad.ID, quota.ID, expired.ID}})
			if err != nil {
				t.Fatal(err)
			}
			bad.ExpiresAt = time.Now().Add(-time.Hour)
			expired.ExpiresAt = bad.ExpiresAt
			quota.BaseQuotaBytes = 10
			for _, u := range []*model.SingBoxUser{bad, quota, expired} {
				if err = s.store.UpdateSingBoxUser(ctx, u); err != nil {
					t.Fatal(err)
				}
			}
			var mu sync.Mutex
			stops := map[int64]bool{}
			sampled := map[int64]bool{}
			ops := singBoxTrafficOps{
				unit: func(child context.Context, _ *model.Node, u *model.SingBoxUser, action string) error {
					if action != "stop" {
						return errors.New("unexpected start")
					}
					if u.ID == bad.ID {
						if timeout {
							<-child.Done()
							return child.Err()
						}
						return errors.New("offline expired member")
					}
					mu.Lock()
					stops[u.ID] = true
					mu.Unlock()
					return nil
				},
				counters: func(_ context.Context, _ *model.Node, id int64) ([2]int64, error) {
					mu.Lock()
					sampled[id] = true
					mu.Unlock()
					return [2]int64{20, 20}, nil
				},
				online: func(context.Context, *model.Node, *model.SingBoxUser) (bool, error) { return false, nil },
			}
			run, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			if err = s.collectSingBoxTraffic(run, ops); err == nil {
				t.Fatal("member failure was not reported")
			}
			if !stops[quota.ID] || !stops[expired.ID] || !sampled[quota.ID] || sampled[bad.ID] || sampled[expired.ID] {
				t.Fatalf("stops=%v samples=%v", stops, sampled)
			}
			got, err := s.store.SingBoxUserByID(ctx, quota.ID)
			if err != nil || got.Enabled || got.Status != model.SingBoxStatusExhausted || got.QuotaPausedAt == nil {
				t.Fatal(got, err)
			}
			got, err = s.store.SingBoxUserByID(ctx, expired.ID)
			if err != nil || got.Enabled || got.Status != model.SingBoxStatusExpired {
				t.Fatal(got, err)
			}
			got, err = s.store.SingBoxUserByID(ctx, bad.ID)
			if err != nil || !got.Enabled {
				t.Fatal("failed remote stop incorrectly marked complete", got, err)
			}
		})
	}
}

func TestSingBoxTrafficRolloverBeforeSampleAndQuotaStopIsolation(t *testing.T) {
	ctx := context.Background()
	s, n := subscriptionTestService(t)
	bad := collectionUser(t, s, n.ID, 10001)
	healthy := collectionUser(t, s, n.ID, 10002)
	bad.BaseQuotaBytes = 10
	healthy.BaseQuotaBytes = 100
	paused := time.Now().Add(-time.Hour)
	healthy.QuotaPausedAt = &paused
	healthy.Enabled = false
	healthy.PeriodEndsAt = paused
	healthy.PeriodStartedAt = paused.Add(-30 * 24 * time.Hour)
	for _, u := range []*model.SingBoxUser{bad, healthy} {
		if err := s.store.UpdateSingBoxUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	events := []string{}
	ops := singBoxTrafficOps{
		unit: func(_ context.Context, _ *model.Node, u *model.SingBoxUser, action string) error {
			if u.ID == bad.ID {
				return errors.New("quota stop offline")
			}
			mu.Lock()
			events = append(events, action)
			mu.Unlock()
			return nil
		},
		counters: func(_ context.Context, _ *model.Node, id int64) ([2]int64, error) {
			if id == healthy.ID {
				mu.Lock()
				events = append(events, "sample")
				mu.Unlock()
			}
			return [2]int64{20, 20}, nil
		},
		online: func(context.Context, *model.Node, *model.SingBoxUser) (bool, error) { return false, nil },
	}
	if err := s.collectSingBoxTraffic(ctx, ops); err == nil {
		t.Fatal("quota stop failure not reported")
	}
	if len(events) != 2 || events[0] != "start" || events[1] != "sample" {
		t.Fatal(events)
	}
	got, err := s.store.SingBoxUserByID(ctx, healthy.ID)
	if err != nil || !got.Enabled || got.QuotaPausedAt != nil || got.RawIn != 20 {
		t.Fatal(got, err)
	}
}
