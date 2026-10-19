package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/singbox"
	"github.com/kukumi1/fluxlite/internal/store"
	"testing"
	"time"
)

func TestSlowSubscriptionCreateDoesNotBlockOtherGroupRevocationAndEnforcement(t *testing.T) {
	s, n := subscriptionTestService(t)
	ctx := context.Background()
	token, hash, e := newSubscriptionToken()
	if e != nil {
		t.Fatal(e)
	}
	past := time.Now().Add(-time.Hour)
	existing := &store.Subscription{Name: "existing", Enabled: true, Status: "active", ExpiresAt: &past}
	if e = s.store.CreateSubscription(ctx, existing, hash); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	u := &model.SingBoxUser{NodeID: n.ID, Name: "existing-user", Protocol: model.SingBoxSS2022, Port: 10000, Source: model.SingBoxManaged, Enabled: true, PeriodStartedAt: now, PeriodEndsAt: now.Add(time.Hour), PeriodDays: 30}
	if e = s.store.CreateSingBoxUser(ctx, u); e != nil {
		t.Fatal(e)
	}
	if e = s.store.AddSubscriptionMember(ctx, existing.ID, store.SubscriptionMember{UserID: u.ID, NodeID: n.ID, Protocol: string(u.Protocol)}); e != nil {
		t.Fatal(e)
	}
	started := make(chan struct{})
	unblock := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = s.createSubscription(ctx, SubscriptionInput{Name: "slow", Entries: []SubscriptionEntry{{NodeID: n.ID, Protocol: model.SingBoxSS2022}}}, func(child context.Context, in SingBoxUserInput) (*model.SingBoxUser, error) {
			if d, ok := child.Deadline(); !ok || time.Until(d) > subscriptionMutationTimeout {
				t.Error("mutation has no total timeout")
			}
			close(started)
			<-unblock
			return nil, errors.New("offline")
		}, func(context.Context, *store.Subscription) error { return nil })
	}()
	<-started
	defer func() { close(unblock); <-done }()
	bounded, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	reset, e := s.ResetSubscriptionToken(bounded, existing.ID)
	if e != nil || reset == token {
		t.Fatalf("unrelated token revocation blocked: %v", e)
	}
	stopped := false
	_ = s.reconcileSubscriptions(bounded, func(ctx context.Context, id int64) error {
		if id == u.ID {
			stopped = true
		}
		return nil
	})
	if !stopped {
		t.Fatal("existing expiry enforcement blocked behind unrelated provisioning")
	}
	got, e := s.store.SubscriptionByID(ctx, existing.ID)
	if e != nil || got.Enabled {
		t.Fatal("existing group not fail-closed")
	}
	all, _ := s.store.ListSubscriptions(ctx)
	for _, v := range all {
		if v.ID != existing.ID && v.Status != "provisioning" {
			t.Fatal("live provisioning reconciled while owner holds its keyed gate")
		}
	}
}
func TestSubscriptionKeyedLockCancellationAndRestartRecovery(t *testing.T) {
	s, _ := subscriptionTestService(t)
	release, e := s.lockSubscription(context.Background(), 1)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = s.lockSubscription(ctx, 1); e == nil {
		t.Fatal("canceled lock acquired")
	}
	other, e := s.lockSubscription(context.Background(), 2)
	if e != nil {
		t.Fatal(e)
	}
	other()
	release()
	s.subscriptionLocksMu.Lock()
	remaining := len(s.subscriptionLocks)
	s.subscriptionLocksMu.Unlock()
	if remaining != 0 {
		t.Fatal("idle keyed lock map leaked")
	}
	hash := sha256.Sum256([]byte("pending"))
	v := &store.Subscription{Name: "interrupted", Enabled: true, Status: "provisioning"}
	if e = s.store.CreateSubscription(context.Background(), v, hash[:]); e != nil {
		t.Fatal(e)
	}
	if e = s.reconcileSubscriptions(context.Background(), func(context.Context, int64) error { return nil }); e != nil {
		t.Fatal(e)
	}
	got, _ := s.store.SubscriptionByID(context.Background(), v.ID)
	if got.Enabled || got.Status != "error" {
		t.Fatal("restart did not fail-close interrupted provisioning")
	}
}
func TestSubscriptionRenderRechecksTokenAfterKeyedLock(t *testing.T) {
	s, n := subscriptionTestService(t)
	ctx := context.Background()
	bundle, e := singbox.Generate(model.SingBoxSS2022, "share", 10001, "/tmp/test", "")
	if e != nil {
		t.Fatal(e)
	}
	u := seedExportBundle(t, s, n.ID, bundle, "member")
	token, hash, e := newSubscriptionToken()
	if e != nil {
		t.Fatal(e)
	}
	v := &store.Subscription{Name: "share", Enabled: true, Status: "active"}
	if e = s.store.CreateSubscription(ctx, v, hash); e != nil {
		t.Fatal(e)
	}
	if e = s.store.AddSubscriptionMember(ctx, v.ID, store.SubscriptionMember{UserID: u.ID, NodeID: n.ID, Protocol: string(u.Protocol)}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RenderSubscription(ctx, 0, token, "base64"); e != nil {
		t.Fatal(e)
	}
	release, e := s.lockSubscription(ctx, v.ID)
	if e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() { _, e := s.RenderSubscription(ctx, 0, token, "base64"); result <- e }()
	deadline := time.Now().Add(time.Second)
	for {
		s.subscriptionLocksMu.Lock()
		waiting := s.subscriptionLocks[v.ID].refs > 1
		s.subscriptionLocksMu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			release()
			t.Fatal("render did not reach keyed lock")
		}
		time.Sleep(time.Millisecond)
	}
	_, next, e := newSubscriptionToken()
	if e != nil {
		release()
		t.Fatal(e)
	}
	if e = s.store.ResetSubscriptionToken(ctx, v.ID, next); e != nil {
		release()
		t.Fatal(e)
	}
	release()
	if e = <-result; !errors.Is(e, store.ErrNotFound) {
		t.Fatalf("render reused token snapshot after revocation: %v", e)
	}
}
