package planner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
)

// fakeLookup is an in-memory NodeLookup for tests.
type fakeLookup struct {
	nodes map[int64]*model.Node
	used  map[int64]map[int]bool
}

func (f *fakeLookup) NodeByID(_ context.Context, id int64) (*model.Node, error) {
	n, ok := f.nodes[id]
	if !ok {
		return nil, errors.New("node not found")
	}
	return n, nil
}

func (f *fakeLookup) UsedPortsOnNode(_ context.Context, nodeID int64, _ *int64) (map[int]bool, error) {
	if f.used[nodeID] == nil {
		return map[int]bool{}, nil
	}
	return f.used[nodeID], nil
}

func probedNode(id int64, name string, start, end int, udp *bool) *model.Node {
	return &model.Node{
		ID:         id,
		Name:       name,
		Host:       name + ".example",
		PortStart:  start,
		PortEnd:    end,
		Arch:       "amd64",
		InitSystem: model.InitSystemd,
		UDPCapable: udp,
	}
}

func hops(nodeIDs ...int64) []model.RouteHop {
	out := make([]model.RouteHop, len(nodeIDs))
	for i, id := range nodeIDs {
		out[i] = model.RouteHop{HopOrder: i, NodeID: id}
	}
	return out
}

func TestAllocateAssignsPortsFromEachPool(t *testing.T) {
	lookup := &fakeLookup{
		nodes: map[int64]*model.Node{
			1: probedNode(1, "entry", 10070, 10086, nil),
			2: probedNode(2, "relay", 20001, 20020, nil),
		},
	}

	got, err := Allocate(context.Background(), lookup, hops(1, 2), model.ProtocolTCP, nil, nil)
	if err != nil {
		t.Fatalf("Allocate returned error: %v", err)
	}
	if got[0].RelayPort != 10070 {
		t.Errorf("entry port = %d, want first port of its pool 10070", got[0].RelayPort)
	}
	if got[1].RelayPort != 20001 {
		t.Errorf("relay port = %d, want first port of its pool 20001", got[1].RelayPort)
	}
}

func TestAllocateSkipsPortsUsedByOtherRoutes(t *testing.T) {
	lookup := &fakeLookup{
		nodes: map[int64]*model.Node{1: probedNode(1, "entry", 10070, 10072, nil)},
		used:  map[int64]map[int]bool{1: {10070: true, 10071: true}},
	}

	got, err := Allocate(context.Background(), lookup, hops(1), model.ProtocolTCP, nil, nil)
	if err != nil {
		t.Fatalf("Allocate returned error: %v", err)
	}
	if got[0].RelayPort != 10072 {
		t.Errorf("port = %d, want 10072 (the only free port)", got[0].RelayPort)
	}
}

// A chain that revisits the same node must not hand out the same port twice,
// which would make one realm instance dial its own listener.
func TestAllocateDoesNotReusePortWithinOneRoute(t *testing.T) {
	lookup := &fakeLookup{
		nodes: map[int64]*model.Node{
			1: probedNode(1, "a", 20001, 20020, nil),
			2: probedNode(2, "b", 30001, 30020, nil),
		},
	}

	got, err := Allocate(context.Background(), lookup, hops(1, 2, 1), model.ProtocolTCP, nil, nil)
	if err != nil {
		t.Fatalf("Allocate returned error: %v", err)
	}
	if got[0].RelayPort == got[2].RelayPort {
		t.Errorf("hops 0 and 2 share node 1 and both got port %d", got[0].RelayPort)
	}
}

func TestAllocateExhaustedPool(t *testing.T) {
	lookup := &fakeLookup{
		nodes: map[int64]*model.Node{1: probedNode(1, "tiny", 10070, 10070, nil)},
		used:  map[int64]map[int]bool{1: {10070: true}},
	}

	_, err := Allocate(context.Background(), lookup, hops(1), model.ProtocolTCP, nil, nil)
	if !errors.Is(err, ErrPortPoolEmpty) {
		t.Fatalf("error = %v, want ErrPortPoolEmpty", err)
	}
}

func TestAllocateRejectsEntryPortOutsidePool(t *testing.T) {
	lookup := &fakeLookup{nodes: map[int64]*model.Node{1: probedNode(1, "entry", 10070, 10086, nil)}}
	requested := 9999

	_, err := Allocate(context.Background(), lookup, hops(1), model.ProtocolTCP, &requested, nil)
	if !errors.Is(err, ErrEntryOutOfPool) {
		t.Fatalf("error = %v, want ErrEntryOutOfPool", err)
	}
}

func TestAllocateRejectsTakenEntryPort(t *testing.T) {
	lookup := &fakeLookup{
		nodes: map[int64]*model.Node{1: probedNode(1, "entry", 10070, 10086, nil)},
		used:  map[int64]map[int]bool{1: {10071: true}},
	}
	requested := 10071

	_, err := Allocate(context.Background(), lookup, hops(1), model.ProtocolTCP, &requested, nil)
	if !errors.Is(err, ErrEntryPortTaken) {
		t.Fatalf("error = %v, want ErrEntryPortTaken", err)
	}
}

// A node proven to drop UDP must not be silently accepted into a udp route:
// the chain would look healthy and carry nothing.
func TestAllocateRejectsUDPRouteThroughUDPBlindNode(t *testing.T) {
	no := false
	lookup := &fakeLookup{
		nodes: map[int64]*model.Node{
			1: probedNode(1, "entry", 10070, 10086, nil),
			2: probedNode(2, "nat", 20001, 20020, &no),
		},
	}

	_, err := Allocate(context.Background(), lookup, hops(1, 2), model.ProtocolTCPUDP, nil, nil)
	if !errors.Is(err, ErrUDPUnsupported) {
		t.Fatalf("error = %v, want ErrUDPUnsupported", err)
	}
}

// An unprobed UDP capability is unknown, not false, so it must not block a
// route the way a proven failure does.
func TestAllocateAllowsUDPRouteWhenCapabilityUnknown(t *testing.T) {
	lookup := &fakeLookup{
		nodes: map[int64]*model.Node{1: probedNode(1, "entry", 10070, 10086, nil)},
	}

	if _, err := Allocate(context.Background(), lookup, hops(1), model.ProtocolTCPUDP, nil, nil); err != nil {
		t.Fatalf("Allocate returned error: %v", err)
	}
}

func TestAllocateRequiresProbedNode(t *testing.T) {
	lookup := &fakeLookup{
		nodes: map[int64]*model.Node{1: {ID: 1, Name: "fresh", PortStart: 1, PortEnd: 10}},
	}

	_, err := Allocate(context.Background(), lookup, hops(1), model.ProtocolTCP, nil, nil)
	if !errors.Is(err, ErrNodeNotProbed) {
		t.Fatalf("error = %v, want ErrNodeNotProbed", err)
	}
}

func TestBuildChainsHopsAndEndsAtTarget(t *testing.T) {
	lookup := &fakeLookup{
		nodes: map[int64]*model.Node{
			1: probedNode(1, "tx", 10070, 10086, nil),
			2: probedNode(2, "cnix", 20001, 20020, nil),
		},
	}
	route := &model.Route{
		ID:       7,
		Name:     "tw-a",
		Target:   "landing.example:31001",
		Protocol: model.ProtocolTCP,
		Hops: []model.RouteHop{
			{HopOrder: 0, NodeID: 1, RelayPort: 10071},
			{HopOrder: 1, NodeID: 2, RelayPort: 20005},
		},
	}

	plan, err := Build(context.Background(), lookup, route)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if plan.Hops[0].Remote != "cnix.example:20005" {
		t.Errorf("hop 0 remote = %q, want the next hop's address", plan.Hops[0].Remote)
	}
	if plan.Hops[1].Remote != "landing.example:31001" {
		t.Errorf("hop 1 remote = %q, want the landing target", plan.Hops[1].Remote)
	}
	if !strings.Contains(plan.Hops[0].Config, `listen = "0.0.0.0:10071"`) {
		t.Errorf("hop 0 config missing its listen directive:\n%s", plan.Hops[0].Config)
	}
}

// The applier skips a node when the hash matches, so identical inputs must
// produce byte-identical configs or every apply would restart every relay.
func TestBuildConfigHashIsStable(t *testing.T) {
	lookup := &fakeLookup{nodes: map[int64]*model.Node{1: probedNode(1, "tx", 10070, 10086, nil)}}
	route := &model.Route{
		ID:       1,
		Name:     "solo",
		Target:   "landing.example:443",
		Protocol: model.ProtocolTCPUDP,
		Hops:     []model.RouteHop{{HopOrder: 0, NodeID: 1, RelayPort: 10070}},
	}

	first, err := Build(context.Background(), lookup, route)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	second, err := Build(context.Background(), lookup, route)
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if first.Hops[0].Hash != second.Hops[0].Hash {
		t.Error("identical routes produced different config hashes")
	}
	if !strings.Contains(first.Hops[0].Config, "use_udp = true") {
		t.Error("tcp+udp route did not enable udp in the rendered config")
	}
}
