package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

func TestSingBoxTrafficAndPeriodRollover(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "fluxlite.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	node := &model.Node{Name: "n", Host: "127.0.0.1", SSHPort: 22, SSHUser: "root", AuthType: model.AuthPassword, AuthSecret: []byte("x"), PortStart: 10000, PortEnd: 10100}
	if err := st.CreateNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-31 * 24 * time.Hour)
	u := &model.SingBoxUser{NodeID: node.ID, Name: "alice", Protocol: model.SingBoxSS2022, Port: 10000, Enabled: true, Source: model.SingBoxManaged, ServiceName: "x", ConfigPath: "x", BaseQuotaBytes: 100, PeriodStartedAt: now, PeriodEndsAt: now.Add(30 * 24 * time.Hour), PeriodDays: 30}
	if err := st.CreateSingBoxUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	got, err := st.RecordSingBoxTraffic(ctx, u.ID, 60, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got.UsedBytes() != 110 || got.QuotaPausedAt == nil {
		t.Fatalf("quota not enforced: %+v", got)
	}
	if err := st.RollSingBoxPeriod(ctx, got, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, err = st.SingBoxUserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UsedBytes() != 0 || got.TopUpBytes != 0 || got.QuotaPausedAt != nil {
		t.Fatalf("period did not reset: %+v", got)
	}
}

func TestReduceSingBoxQuotaUsesTopUpBeforeBase(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "fluxlite.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	node := &model.Node{Name: "n", Host: "127.0.0.1", SSHPort: 22, SSHUser: "root", AuthType: model.AuthPassword, AuthSecret: []byte("x"), PortStart: 10000, PortEnd: 10100}
	if err := st.CreateNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	u := &model.SingBoxUser{NodeID: node.ID, Name: "alice", Protocol: model.SingBoxSS2022, Port: 10000, Enabled: true, Source: model.SingBoxManaged, ServiceName: "x", ConfigPath: "x", BaseQuotaBytes: 100, TopUpBytes: 50, PeriodStartedAt: now, PeriodEndsAt: now.Add(30 * 24 * time.Hour), PeriodDays: 30}
	if err := st.CreateSingBoxUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := st.ReduceSingBoxQuota(ctx, u.ID, 30); err != nil {
		t.Fatal(err)
	}
	got, err := st.SingBoxUserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseQuotaBytes != 100 || got.TopUpBytes != 20 {
		t.Fatalf("reduction did not consume top-up first: %+v", got)
	}
	if err := st.ReduceSingBoxQuota(ctx, u.ID, 20); err != nil {
		t.Fatal(err)
	}
	got, err = st.SingBoxUserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseQuotaBytes != 100 || got.TopUpBytes != 0 {
		t.Fatalf("unexpected quota after top-up was removed: %+v", got)
	}
}
