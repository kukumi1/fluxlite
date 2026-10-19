package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"github.com/kukumi1/fluxlite/internal/model"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectionMigrationPreservesCredentialsAndOnlyEligibleGroups(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	s, e := Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { s.Close() }()
	n := &model.Node{Name: "machine", Host: "127.0.0.1", SSHPort: 22, SSHUser: "root", AuthType: model.AuthPassword, AuthSecret: []byte("ssh-key"), PortStart: 10000, PortEnd: 10100}
	if e = s.CreateNode(ctx, n); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	u := &model.SingBoxUser{NodeID: n.ID, Name: "node", Protocol: model.SingBoxSS2022, Port: 10000, Enabled: true, Source: model.SingBoxManaged, ServiceName: "test", ConfigPath: "test", ConfigBlob: []byte("sealed-node-keys"), PeriodStartedAt: now, PeriodEndsAt: now.Add(time.Hour), PeriodDays: 30}
	if e = s.CreateSingBoxUser(ctx, u); e != nil {
		t.Fatal(e)
	}
	expiry := now.Add(-time.Hour)
	groups := []*Subscription{{Name: "convert", Enabled: true, Status: "active"}, {Name: "capped", Enabled: true, Status: "active", QuotaBytes: 10}, {Name: "expired", Enabled: true, Status: "active", ExpiresAt: &expiry}, {Name: "disabled", Enabled: false, Status: "active"}, {Name: "error", Enabled: true, Status: "error"}, {Name: "provisioning", Enabled: true, Status: "provisioning"}}
	for _, v := range groups {
		hash := sha256.Sum256([]byte(v.Name))
		if e = s.CreateSubscription(ctx, v, hash[:], []byte("sealed-token")); e != nil {
			t.Fatal(e)
		}
	}
	first := groups[0]
	if e = s.AddSubscriptionMember(ctx, first.ID, SubscriptionMember{UserID: u.ID, NodeID: n.ID, Protocol: string(u.Protocol)}); e != nil {
		t.Fatal(e)
	}
	if e = s.RecordSubscriptionCounters(ctx, u.ID, 51, 52); e != nil {
		t.Fatal(e)
	}
	if _, e = s.DB().Exec(`ALTER TABLE subscriptions DROP COLUMN mode; UPDATE schema_version SET version=48`); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	for i, v := range groups {
		got, e := s.SubscriptionByID(ctx, v.ID)
		want := "owned"
		if i == 0 {
			want = "collection"
		}
		if e != nil || got.Mode != want {
			t.Fatal(got, e)
		}
		hash, secret, e := s.SubscriptionTokenMaterial(ctx, v.ID)
		wantHash := sha256.Sum256([]byte(v.Name))
		if e != nil || !bytes.Equal(hash, wantHash[:]) || !bytes.Equal(secret, []byte("sealed-token")) {
			t.Fatal("credential changed", e)
		}
	}
	got, e := s.SubscriptionByID(ctx, first.ID)
	if e != nil || got.UsedBytes != 103 || len(got.Members) != 1 || got.Members[0].UserID != u.ID {
		t.Fatal(got, e)
	}
	child, e := s.SingBoxUserByID(ctx, u.ID)
	if e != nil || !bytes.Equal(child.ConfigBlob, u.ConfigBlob) {
		t.Fatal("node keys changed", e)
	}
	machine, e := s.NodeByID(ctx, n.ID)
	if e != nil || !bytes.Equal(machine.AuthSecret, n.AuthSecret) {
		t.Fatal("SSH key changed", e)
	}
	if e = s.DeleteNode(ctx, n.ID); e != nil {
		t.Fatal("machine deletion must detach collections atomically", e)
	}
	got, e = s.SubscriptionByID(ctx, first.ID)
	if e != nil || len(got.Members) != 0 {
		t.Fatal(got, e)
	}
}
