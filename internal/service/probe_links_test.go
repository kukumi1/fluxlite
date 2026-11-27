package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/store"
)

// probeFixture is one online panel machine with its probe agent running one
// latency check, and a second agent no panel machine is paired with.
func probeFixture(t *testing.T) (*Service, *model.Node) {
	t.Helper()
	s := newReinstallService(t)
	ctx := context.Background()
	n := seedReinstallNode(t, s)
	seen := time.Now()
	if err := s.store.SetNodeStatus(ctx, n.ID, model.StatusOnline, &seen); err != nil {
		t.Fatal(err)
	}
	err := s.store.ReplaceProbe(ctx, store.ProbeImport{
		Clients: []store.ProbeClient{{UUID: "hk", Token: "t-hk", Name: "HK"}, {UUID: "lone", Token: "t-lone", Name: "Lone"}},
		Tasks:   []store.ProbeTask{{ID: 7, Name: "成都联通", Type: "tcp", Target: "192.0.2.1:443", Interval: 60, Clients: []string{"hk", "lone"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetProbeNode(ctx, "hk", &n.ID); err != nil {
		t.Fatal(err)
	}
	return s, n
}

// The latency alerts judge what the probe agents measure: a panel machine's
// checks over the last few minutes, named by machine and check.
func TestLatencyAlertsReadTheProbe(t *testing.T) {
	s, n := probeFixture(t)
	ctx := context.Background()
	now := time.Now()
	minute := now.Add(-time.Minute).Unix() / 60 * 60
	pings := []store.ProbePing{
		{Task: 7, Client: "hk", Bucket: minute, Sent: 4, Lost: 2, Sum: 200},
		{Task: 7, Client: "lone", Bucket: minute, Sent: 4, Lost: 4},
	}
	if err := s.store.AddProbePings(ctx, pings); err != nil {
		t.Fatal(err)
	}
	subjects, err := s.latencySubjects(ctx, now, AlertSettings{LossPercent: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(subjects) != 1 {
		t.Fatalf("only machines the panel knows are judged: %+v", subjects)
	}
	sub := subjects[0]
	if sub.nodeID != n.ID || sub.name != n.Name+" → 成都联通" || sub.state != obsFailing || !strings.Contains(sub.reasons[0], "丢包 50%") {
		t.Fatalf("%+v", sub)
	}
}

// A spike is measured against the same link's usual over the last day.
func TestSpikesReadTheProbe(t *testing.T) {
	s, n := probeFixture(t)
	ctx := context.Background()
	now := time.Now()
	var pings []store.ProbePing
	for at := now.Add(-20 * time.Hour); at.Before(now.Add(-time.Hour)); at = at.Add(5 * time.Minute) {
		pings = append(pings, store.ProbePing{Task: 7, Client: "hk", Bucket: at.Unix() / 300 * 300, Sent: 1, Sum: 50})
	}
	if err := s.store.AddProbePings(ctx, pings); err != nil {
		t.Fatal(err)
	}
	if err := s.store.AddProbePings(ctx, []store.ProbePing{{Task: 7, Client: "hk", Bucket: now.Add(-time.Minute).Unix() / 60 * 60, Sent: 2, Sum: 400}}); err != nil {
		t.Fatal(err)
	}
	subjects, err := s.spikeSubjects(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range subjects {
		if sub.nodeID == n.ID {
			if sub.state != obsFailing || !strings.Contains(sub.reasons[0], "成都联通：200ms（基线 50ms") {
				t.Fatalf("%+v", sub)
			}
			return
		}
	}
	t.Fatal("the machine was not judged")
}
