package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"github.com/kukumi1/fluxlite/internal/model"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionLifetimeAndRevocation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	s, e := Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	n := &model.Node{Name: "n", Host: "127.0.0.1", SSHPort: 22, SSHUser: "root", AuthType: model.AuthPassword, AuthSecret: []byte("x"), PortStart: 10000, PortEnd: 10100}
	if e = s.CreateNode(ctx, n); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	u := &model.SingBoxUser{NodeID: n.ID, Name: "member", Protocol: model.SingBoxSS2022, Port: 10000, Enabled: true, Source: model.SingBoxManaged, ServiceName: "x", ConfigPath: "x", PeriodStartedAt: now.Add(-31 * 24 * time.Hour), PeriodEndsAt: now.Add(-24 * time.Hour), PeriodDays: 30}
	if e = s.CreateSingBoxUser(ctx, u); e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256([]byte("old-token"))
	v := &Subscription{Name: "share", Enabled: true, Status: "active", QuotaBytes: 200}
	if e = s.CreateSubscription(ctx, v, h[:]); e != nil {
		t.Fatal(e)
	}
	if e = s.AddSubscriptionMember(ctx, v.ID, SubscriptionMember{u.ID, n.ID, string(u.Protocol)}); e != nil {
		t.Fatal(e)
	}
	for _, sample := range [][2]int64{{40, 20}, {40, 20}, {70, 30}} {
		if e = s.RecordSubscriptionCounters(ctx, u.ID, sample[0], sample[1]); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.RollSingBoxPeriod(ctx, u, now); e != nil {
		t.Fatal(e)
	}
	if e = s.RecordSubscriptionCounters(ctx, u.ID, 90, 40); e != nil {
		t.Fatal(e)
	}
	if e = s.DeleteSingBoxUser(ctx, u.ID); e == nil {
		t.Fatal("member user deletion not restricted")
	}
	if e = s.DeleteNode(ctx, n.ID); e == nil {
		t.Fatal("member node deletion not restricted")
	}
	s.Close()
	s, e = Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	got, e := s.SubscriptionByHash(ctx, h[:])
	if e != nil {
		t.Fatal(e)
	}
	if got.UsedBytes != 130 {
		t.Fatalf("durable usage %d", got.UsedBytes)
	}
	if e = s.RecordSubscriptionCounters(ctx, u.ID, 10, 5); e != nil {
		t.Fatal(e)
	}
	got, _ = s.SubscriptionByID(ctx, v.ID)
	if got.UsedBytes != 145 {
		t.Fatalf("counter restart usage %d", got.UsedBytes)
	}
	next := sha256.Sum256([]byte("new-token"))
	if e = s.ResetSubscriptionToken(ctx, v.ID, next[:]); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SubscriptionByHash(ctx, h[:]); e == nil {
		t.Fatal("old token remained valid")
	}
	if _, e = s.SubscriptionByHash(ctx, next[:]); e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "token") || strings.Contains(string(b), "hash") {
		t.Fatalf("credential leak %s", b)
	}
	expired := now.Add(-time.Second)
	got.ExpiresAt = &expired
	if got.Allowed(now) {
		t.Fatal("expired allowed")
	}
	got.ExpiresAt = nil
	got.QuotaBytes = 145
	if got.Allowed(now) {
		t.Fatal("exhausted allowed")
	}
	got.QuotaBytes = 0
	got.Enabled = false
	if got.Allowed(now) {
		t.Fatal("disabled allowed")
	}
	got.Enabled = true
	got.Status = "error"
	if got.Allowed(now) {
		t.Fatal("error allowed")
	}
}
func TestSubscriptionMissingMeasurementFailsClosed(t *testing.T) {
	ctx := context.Background()
	s, e := Open(ctx, filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	n := &model.Node{Name: "node", Host: "127.0.0.1", SSHPort: 22, SSHUser: "root", AuthType: model.AuthPassword, AuthSecret: []byte("x"), PortStart: 10000, PortEnd: 10100}
	if e = s.CreateNode(ctx, n); e != nil {
		t.Fatal(e)
	}
	for i, quota := range []int64{100, 0} {
		now := time.Now()
		u := &model.SingBoxUser{NodeID: n.ID, Name: []string{"finite", "unlimited"}[i], Protocol: model.SingBoxSS2022, Port: 10000 + i, Source: model.SingBoxManaged, PeriodStartedAt: now, PeriodEndsAt: now.Add(time.Hour), PeriodDays: 30}
		if e = s.CreateSingBoxUser(ctx, u); e != nil {
			t.Fatal(e)
		}
		hash := sha256.Sum256([]byte(u.Name))
		v := &Subscription{Name: u.Name, Enabled: true, Status: "active", QuotaBytes: quota}
		if e = s.CreateSubscription(ctx, v, hash[:]); e != nil {
			t.Fatal(e)
		}
		if e = s.AddSubscriptionMember(ctx, v.ID, SubscriptionMember{u.ID, n.ID, string(u.Protocol)}); e != nil {
			t.Fatal(e)
		}
		if e = s.FailSubscriptionMeasurement(ctx, u.ID); e != nil {
			t.Fatal(e)
		}
		got, e := s.SubscriptionByID(ctx, v.ID)
		if e != nil {
			t.Fatal(e)
		}
		if quota > 0 {
			if got.Enabled || got.Status != "error" || got.Allowed(now) {
				t.Fatal("finite quota allowed unknown traffic")
			}
		} else if !got.Allowed(now) {
			t.Fatal("unlimited subscription incorrectly blocked")
		}
	}
}
