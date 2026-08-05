package store

import (
	"context"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
)

func seedRoute(t *testing.T, st *Store) *model.Route {
	t.Helper()
	ctx := context.Background()

	var nodeIDs []int64
	for _, name := range []string{"entry", "exit"} {
		n := &model.Node{
			Name: name, Host: "203.0.113.1", SSHPort: 22, SSHUser: "root",
			AuthType: model.AuthPassword, AuthSecret: []byte("x"),
			PortStart: 10000, PortEnd: 65535,
		}
		if err := st.CreateNode(ctx, n); err != nil {
			t.Fatalf("create node %s: %v", name, err)
		}
		nodeIDs = append(nodeIDs, n.ID)
	}

	r := &model.Route{
		Name: "chain", Slug: "chain", Target: "example.com:443",
		Protocol: model.ProtocolTCP, Enabled: true, EntryPort: 10001,
		Hops: []model.RouteHop{
			{HopOrder: 0, NodeID: nodeIDs[0], RelayPort: 10001},
			{HopOrder: 1, NodeID: nodeIDs[1], RelayPort: 10002},
		},
	}
	if err := st.CreateRoute(ctx, r); err != nil {
		t.Fatalf("create route: %v", err)
	}
	return r
}

func TestHopLatencyRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	route := seedRoute(t, st)

	// A fresh route has no measurement, and that must stay distinguishable
	// from a measurement of zero.
	loaded, err := st.RouteByID(ctx, route.ID)
	if err != nil {
		t.Fatalf("load route: %v", err)
	}
	for _, h := range loaded.Hops {
		if h.LatencyMS != nil {
			t.Errorf("hop %d reports latency %d before any measurement", h.HopOrder, *h.LatencyMS)
		}
		if h.LatencyAt != nil {
			t.Errorf("hop %d reports a measurement time before any measurement", h.HopOrder)
		}
	}

	if err := st.SetHopLatencies(ctx, route.ID, map[int]int{0: 25, 1: 15}); err != nil {
		t.Fatalf("set latencies: %v", err)
	}

	loaded, err = st.RouteByID(ctx, route.ID)
	if err != nil {
		t.Fatalf("reload route: %v", err)
	}
	want := map[int]int{0: 25, 1: 15}
	for _, h := range loaded.Hops {
		if h.LatencyMS == nil {
			t.Fatalf("hop %d lost its latency", h.HopOrder)
		}
		if *h.LatencyMS != want[h.HopOrder] {
			t.Errorf("hop %d latency = %d, want %d", h.HopOrder, *h.LatencyMS, want[h.HopOrder])
		}
		if h.LatencyAt == nil {
			t.Errorf("hop %d has a latency but no measurement time", h.HopOrder)
		}
	}
}

// One probe that could not time a hop must not erase what an earlier probe
// learned about it.
func TestSetHopLatenciesLeavesUnreportedHopsAlone(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	route := seedRoute(t, st)

	if err := st.SetHopLatencies(ctx, route.ID, map[int]int{0: 25, 1: 15}); err != nil {
		t.Fatalf("first measurement: %v", err)
	}
	if err := st.SetHopLatencies(ctx, route.ID, map[int]int{0: 30}); err != nil {
		t.Fatalf("second measurement: %v", err)
	}

	loaded, err := st.RouteByID(ctx, route.ID)
	if err != nil {
		t.Fatalf("reload route: %v", err)
	}
	got := map[int]int{}
	for _, h := range loaded.Hops {
		if h.LatencyMS != nil {
			got[h.HopOrder] = *h.LatencyMS
		}
	}
	if got[0] != 30 {
		t.Errorf("hop 0 latency = %d, want the fresh 30", got[0])
	}
	if got[1] != 15 {
		t.Errorf("hop 1 latency = %d, want the retained 15", got[1])
	}
}

// Editing a route rebuilds its hops, so stale timings for a path that no
// longer exists must not survive.
func TestEditingARouteClearsStaleLatencies(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	route := seedRoute(t, st)

	if err := st.SetHopLatencies(ctx, route.ID, map[int]int{0: 25, 1: 15}); err != nil {
		t.Fatalf("measure: %v", err)
	}

	route.Hops[1].RelayPort = 10003
	if err := st.UpdateRoute(ctx, route); err != nil {
		t.Fatalf("update route: %v", err)
	}

	loaded, err := st.RouteByID(ctx, route.ID)
	if err != nil {
		t.Fatalf("reload route: %v", err)
	}
	for _, h := range loaded.Hops {
		if h.LatencyMS != nil {
			t.Errorf("hop %d kept latency %d across a path change", h.HopOrder, *h.LatencyMS)
		}
	}
}
