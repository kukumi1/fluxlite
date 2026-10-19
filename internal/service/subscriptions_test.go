package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/singbox"
	"github.com/kukumi1/fluxlite/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func subscriptionTestService(t *testing.T) (*Service, *model.Node) {
	t.Helper()
	st, e := store.Open(context.Background(), filepath.Join(t.TempDir(), "db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.Close() })
	n := &model.Node{Name: "machine", Host: "127.0.0.1", SSHPort: 22, SSHUser: "root", AuthType: model.AuthPassword, AuthSecret: []byte("x"), PortStart: 10000, PortEnd: 10100}
	if e = st.CreateNode(context.Background(), n); e != nil {
		t.Fatal(e)
	}
	sealer, e := cryptox.NewSealer(bytes.Repeat([]byte{9}, cryptox.MasterKeySize))
	if e != nil {
		t.Fatal(e)
	}
	return &Service{store: st, sealer: sealer}, n
}
func TestSubscriptionProvisioningIndependentAndPartialFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "independent", true: "partial"}[fail], func(t *testing.T) {
			s, n := subscriptionTestService(t)
			ctx := context.Background()
			calls := 0
			var records []*model.SingBoxUser
			create := func(ctx context.Context, in SingBoxUserInput) (*model.SingBoxUser, error) {
				calls++
				if fail && calls == 2 {
					return nil, errors.New("SSH failed with sensitive diagnostic")
				}
				now := time.Now()
				u := &model.SingBoxUser{NodeID: in.NodeID, Name: in.Name, Protocol: in.Protocol, Port: 10000 + calls, Enabled: true, Source: model.SingBoxManaged, Status: model.SingBoxStatusStopped, ServiceName: "test", ConfigPath: "test", ConfigBlob: []byte(in.Name), PeriodStartedAt: now, PeriodEndsAt: now.Add(30 * 24 * time.Hour), PeriodDays: 30}
				if e := s.store.CreateSingBoxUser(ctx, u); e != nil {
					return nil, e
				}
				records = append(records, u)
				return u, nil
			}
			cleanupCalled := false
			cleanup := func(ctx context.Context, v *store.Subscription) error {
				cleanupCalled = true
				if ctx.Err() != nil {
					t.Fatal("cleanup context canceled")
				}
				return errors.New("offline; retain members")
			}
			v, token, e := s.createSubscription(ctx, SubscriptionInput{Name: "share", Entries: []SubscriptionEntry{{NodeID: n.ID, Protocol: model.SingBoxSS2022}, {NodeID: n.ID, Protocol: model.SingBoxSS2022}}}, create, cleanup)
			if fail {
				if e == nil || token != "" || !cleanupCalled {
					t.Fatalf("failure not contained: %v", e)
				}
				got, _ := s.store.SubscriptionByID(ctx, v.ID)
				if got.Enabled || got.Status != "error" || len(got.Members) != 1 {
					t.Fatalf("partial state %+v", got)
				}
				if e = s.subscriptionStartAllowed(ctx, got.Members[0].UserID); !errors.Is(e, ErrSubscriptionPolicy) {
					t.Fatalf("failed member start allowed: %v", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if len(token) != 43 || len(records) != 2 || records[0].ID == records[1].ID || records[0].Name == records[1].Name {
				t.Fatal("members not independent")
			}
			h := sha256.Sum256([]byte(token))
			got, e := s.store.SubscriptionByHash(ctx, h[:])
			if e != nil || !got.Allowed(time.Now()) {
				t.Fatal("created token unusable")
			}
			if e = s.subscriptionIndependentOnly(ctx, records[0].ID); !errors.Is(e, ErrSubscriptionPolicy) {
				t.Fatal("managed child edits allowed")
			}
			newToken, e := s.ResetSubscriptionToken(ctx, v.ID)
			if e != nil || newToken == token {
				t.Fatal("token reset failed")
			}
			if _, e = s.store.SubscriptionByHash(ctx, h[:]); e == nil {
				t.Fatal("old token usable")
			}
		})
	}
}
func TestSubscriptionValidationBeforeProvision(t *testing.T) {
	s, n := subscriptionTestService(t)
	calls := 0
	create := func(context.Context, SingBoxUserInput) (*model.SingBoxUser, error) { calls++; return nil, nil }
	cleanup := func(context.Context, *store.Subscription) error { return nil }
	past := time.Now().Add(-time.Hour)
	for _, in := range []SubscriptionInput{{Name: "x"}, {Name: "x", Entries: make([]SubscriptionEntry, 21)}, {Name: "x", QuotaBytes: -1, Entries: []SubscriptionEntry{{NodeID: n.ID, Protocol: model.SingBoxSS2022}}}, {Name: "x", ExpiresAt: &past, Entries: []SubscriptionEntry{{NodeID: n.ID, Protocol: model.SingBoxSS2022}}}, {Name: "x", Entries: []SubscriptionEntry{{NodeID: n.ID, Protocol: "invalid"}}}} {
		if _, _, e := s.createSubscription(context.Background(), in, create, cleanup); e == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid input touched remote")
	}
}
func TestSubscriptionOwnershipPrecedesRemoteDeployment(t *testing.T) {
	st, svc := newTestLookup(t)
	node := seedNode(t, st, "subscription-reserve")
	sealer, e := cryptox.NewSealer(bytes.Repeat([]byte{8}, cryptox.MasterKeySize))
	if e != nil {
		t.Fatal(e)
	}
	svc.sealer = sealer
	ctx := context.Background()
	parent := &store.Subscription{Name: "share", Enabled: true, Status: "provisioning"}
	if e = st.CreateSubscription(ctx, parent, bytes.Repeat([]byte{1}, 32)); e != nil {
		t.Fatal(e)
	}
	ops := singBoxCreateOps{ports: func(context.Context, *model.Node) (map[int]bool, error) { return nil, nil }, deploy: func(ctx context.Context, n *model.Node, u *model.SingBoxUser, b *singbox.Bundle) error {
		owner, e := st.SubscriptionForUser(ctx, u.ID)
		if e != nil || owner == nil || owner.ID != parent.ID {
			t.Fatalf("remote touched before durable ownership: %v", e)
		}
		return errors.New("deployment failed")
	}, cleanup: func(context.Context, *model.Node, *model.SingBoxUser) error { return nil }}
	u, e := svc.createSingBoxUser(ctx, SingBoxUserInput{subscriptionID: parent.ID, NodeID: node.ID, Name: "member", Protocol: model.SingBoxSS2022}, ops)
	if e == nil || u != nil {
		t.Fatal("failed deployment not rolled back")
	}
	got, e := st.SubscriptionByID(ctx, parent.ID)
	if e != nil || len(got.Members) != 0 {
		t.Fatalf("rolled back membership remains: %v", e)
	}
}

func TestSubscriptionStoppedStatusRequiresEveryVerifiedStop(t *testing.T) {
	s, n := subscriptionTestService(t)
	ctx := context.Background()
	groups := []*store.Subscription{}
	var failID int64
	for i, status := range []string{"stopping", "stopping", "error"} {
		_, hash, e := newSubscriptionToken()
		if e != nil {
			t.Fatal(e)
		}
		v := &store.Subscription{Name: fmt.Sprintf("group-%d", i), Enabled: false, Status: status}
		if e = s.store.CreateSubscription(ctx, v, hash); e != nil {
			t.Fatal(e)
		}
		for j := 0; j < 2; j++ {
			now := time.Now()
			u := &model.SingBoxUser{NodeID: n.ID, Name: fmt.Sprintf("member-%d-%d", i, j), Protocol: model.SingBoxSS2022, Port: 10000 + i*2 + j, Source: model.SingBoxManaged, Enabled: false, PeriodStartedAt: now, PeriodEndsAt: now.Add(time.Hour), PeriodDays: 30}
			if e = s.store.CreateSingBoxUser(ctx, u); e != nil {
				t.Fatal(e)
			}
			m := store.SubscriptionMember{UserID: u.ID, NodeID: n.ID, Protocol: string(u.Protocol)}
			if e = s.store.AddSubscriptionMember(ctx, v.ID, m); e != nil {
				t.Fatal(e)
			}
			v.Members = append(v.Members, m)
			if i == 1 && j == 1 {
				failID = u.ID
			}
		}
		groups = append(groups, v)
	}
	err := s.reconcileSubscriptions(ctx, func(_ context.Context, id int64) error {
		if id == failID {
			return errors.New("offline")
		}
		return nil
	})
	if err == nil {
		t.Fatal("partial stop reported success")
	}
	for i, want := range []string{"stopped", "stopping", "error"} {
		got, e := s.store.SubscriptionByID(ctx, groups[i].ID)
		if e != nil || got.Status != want {
			t.Fatalf("group %d status=%v error=%v want %s", i, got, e, want)
		}
	}
	if e := s.reconcileSubscriptions(ctx, func(context.Context, int64) error { return nil }); e != nil {
		t.Fatal(e)
	}
	got, _ := s.store.SubscriptionByID(ctx, groups[1].ID)
	if got.Status != "stopped" {
		t.Fatal("successful retry left pending status")
	}
	got, _ = s.store.SubscriptionByID(ctx, groups[2].ID)
	if got.Status != "error" {
		t.Fatal("successful stops erased diagnosis")
	}
	v := groups[0]
	v.Status = "stopping"
	if e := s.store.UpdateSubscription(ctx, v); e != nil {
		t.Fatal(e)
	}
	if e := s.stopSubscriptionWith(ctx, v, func(context.Context, int64) error { return nil }); e != nil || v.Status != "stopped" {
		t.Fatalf("disable response did not reflect verified stop: %+v %v", v, e)
	}
	v.Status = "stopping"
	if e := s.store.UpdateSubscription(ctx, v); e != nil {
		t.Fatal(e)
	}
	if e := s.stopSubscriptionWith(ctx, v, func(context.Context, int64) error { return errors.New("not verified") }); e == nil {
		t.Fatal("failed stop accepted")
	}
	got, _ = s.store.SubscriptionByID(ctx, v.ID)
	if got.Status != "stopping" {
		t.Fatal("disabled flag mistaken for remote stop proof")
	}
}

func TestSubscriptionCustomPortPassedAndValidated(t *testing.T) {
	s, n := subscriptionTestService(t)
	ctx := context.Background()
	calls := 0
	create := func(ctx context.Context, in SingBoxUserInput) (*model.SingBoxUser, error) {
		calls++
		if in.Port != 24567 {
			t.Fatalf("custom port not forwarded: %d", in.Port)
		}
		return nil, errors.New("fixture stops before remote provision")
	}
	cleanup := func(context.Context, *store.Subscription) error { return nil }
	for _, entries := range [][]SubscriptionEntry{
		{{NodeID: n.ID, Protocol: model.SingBoxAnyTLS, Port: -1}},
		{{NodeID: n.ID, Protocol: model.SingBoxAnyTLS, Port: 65536}},
		{{NodeID: n.ID, Protocol: model.SingBoxAnyTLS, Port: 24567}, {NodeID: n.ID, Protocol: model.SingBoxTrojan, Port: 24567}},
	} {
		if _, _, e := s.createSubscription(ctx, SubscriptionInput{Name: "invalid-port", Entries: entries}, create, cleanup); e == nil {
			t.Fatal("invalid port input accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid input touched provisioning")
	}
	_, _, _ = s.createSubscription(ctx, SubscriptionInput{Name: "custom", Entries: []SubscriptionEntry{{NodeID: n.ID, Protocol: model.SingBoxAnyTLS, Port: 24567}}}, create, cleanup)
	if calls != 1 {
		t.Fatal("custom port creation not reached")
	}
}
