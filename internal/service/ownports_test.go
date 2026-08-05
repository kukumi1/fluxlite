package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/store"
)

func newTestLookup(t *testing.T) (*store.Store, *Service) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, &Service{store: st}
}

func seedNode(t *testing.T, st *store.Store, name string) *model.Node {
	t.Helper()
	n := &model.Node{
		Name: name, Host: "203.0.113.10", SSHPort: 22, SSHUser: "root",
		AuthType: model.AuthPassword, AuthSecret: []byte("x"),
		PortStart: 10000, PortEnd: 65535,
	}
	if err := st.CreateNode(context.Background(), n); err != nil {
		t.Fatalf("create node: %v", err)
	}
	return n
}

// A deployed route keeps its listeners open, and the live port scan cannot
// tell whose they are. If those count as occupied, the route can never be
// edited again: its own entry port comes back as already claimed.
func TestOwnPortsExcludeTheRouteBeingEdited(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestLookup(t)

	entry := seedNode(t, st, "entry")
	exit := seedNode(t, st, "exit")

	route := &model.Route{
		Name: "hk-tw", Slug: "hk-tw", Target: "example.com:443",
		Protocol: model.ProtocolTCP, Enabled: true, EntryPort: 31101,
		Hops: []model.RouteHop{
			{HopOrder: 0, NodeID: entry.ID, RelayPort: 31101},
			{HopOrder: 1, NodeID: exit.ID, RelayPort: 10001},
		},
	}
	if err := st.CreateRoute(ctx, route); err != nil {
		t.Fatalf("create route: %v", err)
	}

	lookup := svc.newLiveLookup(&route.ID)
	// Stand in for the live scan: both relays are running, plus sshd.
	lookup.cache[entry.ID] = portClaims{sockets: map[int]bool{22: true, 31101: true}}
	lookup.cache[exit.ID] = portClaims{sockets: map[int]bool{22: true, 10001: true}}

	used, err := lookup.UsedPortsOnNode(ctx, entry.ID, &route.ID)
	if err != nil {
		t.Fatalf("used ports: %v", err)
	}
	if used[31101] {
		t.Error("the route's own entry port counts as occupied; it could never be edited")
	}
	if !used[22] {
		t.Error("sshd's port must still count as occupied")
	}

	used, err = lookup.UsedPortsOnNode(ctx, exit.ID, &route.ID)
	if err != nil {
		t.Fatalf("used ports: %v", err)
	}
	if used[10001] {
		t.Error("the route's own relay port on a middle hop counts as occupied")
	}
}

// Another route's listeners are not the edited route's to reuse.
func TestOwnPortsDoNotLeakAcrossRoutes(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestLookup(t)
	node := seedNode(t, st, "shared")

	mine := &model.Route{
		Name: "mine", Slug: "mine", Target: "example.com:443",
		Protocol: model.ProtocolTCP, Enabled: true, EntryPort: 10001,
		Hops: []model.RouteHop{{HopOrder: 0, NodeID: node.ID, RelayPort: 10001}},
	}
	theirs := &model.Route{
		Name: "theirs", Slug: "theirs", Target: "example.com:443",
		Protocol: model.ProtocolTCP, Enabled: true, EntryPort: 10002,
		Hops: []model.RouteHop{{HopOrder: 0, NodeID: node.ID, RelayPort: 10002}},
	}
	for _, r := range []*model.Route{mine, theirs} {
		if err := st.CreateRoute(ctx, r); err != nil {
			t.Fatalf("create route %s: %v", r.Name, err)
		}
	}

	lookup := svc.newLiveLookup(&mine.ID)
	lookup.cache[node.ID] = portClaims{sockets: map[int]bool{10001: true, 10002: true}}

	used, err := lookup.UsedPortsOnNode(ctx, node.ID, &mine.ID)
	if err != nil {
		t.Fatalf("used ports: %v", err)
	}
	if used[10001] {
		t.Error("own port must be free to keep")
	}
	if !used[10002] {
		t.Error("another route's port must stay occupied")
	}
}

// Creating a route excludes nothing, so every live listener counts.
func TestLiveListenersCountWhenNothingIsExcluded(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestLookup(t)
	node := seedNode(t, st, "fresh")

	lookup := svc.newLiveLookup(nil)
	lookup.cache[node.ID] = portClaims{sockets: map[int]bool{22: true, 10001: true}}

	used, err := lookup.UsedPortsOnNode(ctx, node.ID, nil)
	if err != nil {
		t.Fatalf("used ports: %v", err)
	}
	for _, p := range []int{22, 10001} {
		if !used[p] {
			t.Errorf("port %d should count as occupied", p)
		}
	}
}

// A NAT claim never belongs to fluxlite — it deploys relays, not rules — so
// the exemption that lets a route keep its own listeners must not extend to
// it. Handing back a diverted port would produce a relay that binds, looks
// healthy, and never sees the traffic aimed at it.
func TestNATClaimsAreNeverExempted(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestLookup(t)
	node := seedNode(t, st, "shared")

	route := &model.Route{
		Name: "mine", Slug: "mine", Target: "example.com:443",
		Protocol: model.ProtocolTCP, Enabled: true, EntryPort: 31001,
		Hops: []model.RouteHop{{HopOrder: 0, NodeID: node.ID, RelayPort: 31001}},
	}
	if err := st.CreateRoute(ctx, route); err != nil {
		t.Fatalf("create route: %v", err)
	}

	lookup := svc.newLiveLookup(&route.ID)
	lookup.cache[node.ID] = portClaims{
		sockets: map[int]bool{31001: true},
		nat:     map[int]bool{31001: true},
	}

	used, err := lookup.UsedPortsOnNode(ctx, node.ID, &route.ID)
	if err != nil {
		t.Fatalf("used ports: %v", err)
	}
	if !used[31001] {
		t.Error("a port diverted by NAT was offered for allocation")
	}
}
