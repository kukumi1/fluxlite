package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/singbox"
	"github.com/kukumi1/fluxlite/internal/store"
	"strings"
	"sync"
	"testing"
	"time"
)

func collectionUser(t *testing.T, s *Service, nodeID int64, port int) *model.SingBoxUser {
	t.Helper()
	b, e := singbox.Generate(model.SingBoxSS2022, "selected", port, "/etc/test", "")
	if e != nil {
		t.Fatal(e)
	}
	return seedExportBundle(t, s, nodeID, b, fmt.Sprintf("selected-%d", port))
}
func TestCollectionLocalLifecycleAndNodePolicy(t *testing.T) {
	ctx := context.Background()
	s, n := subscriptionTestService(t)
	u := collectionUser(t, s, n.ID, 10001)
	other := collectionUser(t, s, n.ID, 10002)
	original := append([]byte(nil), u.ConfigBlob...)
	v, token, e := s.CreateSubscription(ctx, SubscriptionInput{Name: "collection", UserIDs: []int64{u.ID, other.ID}})
	if e != nil {
		t.Fatal(e)
	}
	if v.Mode != "collection" {
		t.Fatal(v)
	}
	if e = s.subscriptionIndependentOnly(ctx, u.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.UpdateSubscription(ctx, v.ID, SubscriptionPatch{}); !errors.Is(e, ErrSubscriptionPolicy) {
		t.Fatal(e)
	}
	if e = s.store.RecordSubscriptionCounters(ctx, u.ID, 100, 100); e != nil {
		t.Fatal(e)
	}
	got, e := s.store.SubscriptionByID(ctx, v.ID)
	if e != nil || got.UsedBytes != 0 {
		t.Fatal(got, e)
	}
	u.Name = "renamed"
	u.BaseQuotaBytes = 10
	if e = s.store.UpdateSingBoxUser(ctx, u); e != nil {
		t.Fatal(e)
	}
	other.Enabled = false
	if e = s.store.UpdateSingBoxUser(ctx, other); e != nil {
		t.Fatal(e)
	}
	result, e := s.RenderSubscription(ctx, 0, token, "clash")
	if e != nil || result.IncludedCount != 1 || !strings.Contains(string(result.Content), "renamed") {
		t.Fatalf("%+v %v", result, e)
	}
	// The disabled member is no longer served but still counted, and being
	// unlimited it leaves the subscription unlimited.
	if result.Name != "collection" || result.Usage.Total != 0 || result.Usage.Expire != nil {
		t.Fatalf("usage %+v name %q", result.Usage, result.Name)
	}
	updated, e := s.store.RecordSingBoxTraffic(ctx, u.ID, 20, 20)
	if e != nil || updated.QuotaPausedAt == nil {
		t.Fatal(updated, e)
	}
	// With no node left to serve, the link still answers so clients can show
	// that the traffic ran out.
	result, e = s.RenderSubscription(ctx, 0, token, "clash")
	if e != nil || result.IncludedCount != 0 || result.Usage.Upload+result.Usage.Download != 40 || !strings.Contains(string(result.Content), "DIRECT") {
		t.Fatalf("%+v %v", result, e)
	}
	// Disabled group state cannot prevent standalone export or per-node controls.
	v.Enabled = false
	v.Status = "error"
	if e = s.store.UpdateSubscription(ctx, v); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ExportSingBoxBundle(ctx, u.ID, n.Host); e != nil {
		t.Fatal("collection group state blocked standalone export", e)
	}
	if e = s.subscriptionStartAllowed(ctx, u.ID); e != nil {
		t.Fatal(e)
	}
	if e = s.reconcileSubscriptions(ctx, func(context.Context, int64) error { t.Fatal("collection stopped a node"); return nil }); e != nil {
		t.Fatal(e)
	}
	if e = s.CollectSubscriptionTraffic(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.DeleteSubscription(ctx, v.ID); e != nil {
		t.Fatal(e)
	}
	remaining, e := s.store.SingBoxUserByID(ctx, u.ID)
	if e != nil || !bytes.Equal(remaining.ConfigBlob, original) {
		t.Fatal("node changed", e)
	}
	if _, _, e = s.CreateSubscription(ctx, SubscriptionInput{Name: "freed", UserIDs: []int64{u.ID}}); e != nil {
		t.Fatal(e)
	}
	if e = s.store.DeleteSingBoxUser(ctx, u.ID); e != nil {
		t.Fatal(e)
	}
}
func TestCollectionExclusivityAndAtomicRollback(t *testing.T) {
	ctx := context.Background()
	s, n := subscriptionTestService(t)
	u := collectionUser(t, s, n.ID, 10001)
	other := collectionUser(t, s, n.ID, 10002)
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, e := s.CreateSubscription(ctx, SubscriptionInput{Name: "race", UserIDs: []int64{u.ID}})
			out <- e
		}()
	}
	wg.Wait()
	close(out)
	wins := 0
	for e := range out {
		if e == nil {
			wins++
		} else if !errors.Is(e, store.ErrConflict) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatal(wins)
	}
	for _, ids := range [][]int64{{other.ID, u.ID}, {other.ID, 99999}, {other.ID, other.ID}} {
		if _, _, e := s.CreateSubscription(ctx, SubscriptionInput{Name: "rollback", UserIDs: ids}); e == nil {
			t.Fatal("invalid accepted")
		}
	}
	owner, e := s.store.SubscriptionForUser(ctx, other.ID)
	if e != nil || owner != nil {
		t.Fatal("partial occupancy", e)
	}
	all, e := s.ListSubscriptions(ctx)
	if e != nil || len(all) != 1 {
		t.Fatal("partial subscription", e)
	}
	other.Source = model.SingBoxExternal
	if e = s.store.UpdateSingBoxUser(ctx, other); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.CreateSubscription(ctx, SubscriptionInput{Name: "external", UserIDs: []int64{other.ID}}); !errors.Is(e, store.ErrConflict) {
		t.Fatal(e)
	}
	candidates, e := s.SubscriptionCandidates(ctx)
	if e != nil || len(candidates) != 1 || candidates[0].SubscriptionID == nil {
		t.Fatal(candidates, e)
	}
}
func TestCollectionRegularSamplerProcessesMember(t *testing.T) {
	ctx := context.Background()
	s, n := subscriptionTestService(t)
	u := collectionUser(t, s, n.ID, 10001)
	_, _, e := s.CreateSubscription(ctx, SubscriptionInput{Name: "collection", UserIDs: []int64{u.ID}})
	if e != nil {
		t.Fatal(e)
	}
	u.Enabled = false
	u.ExpiresAt = time.Now().Add(-time.Hour)
	if e = s.store.UpdateSingBoxUser(ctx, u); e != nil {
		t.Fatal(e)
	}
	if e = s.CollectSingBoxTraffic(ctx); e != nil {
		t.Fatal(e)
	}
	u, e = s.store.SingBoxUserByID(ctx, u.ID)
	if e != nil || u.Status != model.SingBoxStatusExpired {
		t.Fatal(u, e)
	}
}
