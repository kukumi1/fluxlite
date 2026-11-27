package probe

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/store"
)

func node(t *testing.T, st *store.Store, name, host string) *model.Node {
	t.Helper()
	n := &model.Node{Name: name, Host: host, SSHPort: 22, SSHUser: "root", AuthType: model.AuthKey, AuthSecret: []byte("secret"), PortStart: 10000, PortEnd: 20000}
	if err := st.CreateNode(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	return n
}

func clientsByUUID(t *testing.T, st *store.Store) map[string]store.ProbeClient {
	t.Helper()
	list, err := st.ProbeClients(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.ProbeClient{}
	for _, c := range list {
		out[c.UUID] = c
	}
	return out
}

// Probe machines are paired with the panel's machine at their address; an
// address two machines share, or a pairing the operator chose, is left alone.
func TestLinkMatchesByAddress(t *testing.T) {
	h, st, _ := fixture(t)
	ctx := context.Background()
	info := func(v4, v6 string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"ipv4": v4, "ipv6": v6})
		return b
	}
	err := st.ReplaceProbe(ctx, store.ProbeImport{Clients: []store.ProbeClient{
		{UUID: "hk", Token: "t-hk", Name: "HK", Info: info("192.0.2.10", "")},
		{UUID: "jp", Token: "t-jp", Name: "JP", Info: info("", "2001:db8::0:1")},
		{UUID: "nat", Token: "t-nat", Name: "NAT", Info: info("192.0.2.30", "")},
		{UUID: "us", Token: "t-us", Name: "US", Info: info("192.0.2.40", "")},
		{UUID: "agent-only", Token: "t-x", Name: "X", Info: info("198.51.100.9", "")},
	}})
	if err != nil {
		t.Fatal(err)
	}
	hk := node(t, st, "hk", "192.0.2.10")
	jp := node(t, st, "jp", "2001:db8::1")
	node(t, st, "nat-a", "192.0.2.30")
	node(t, st, "nat-b", "192.0.2.30")
	us := node(t, st, "us", "192.0.2.40")
	if err := st.SetProbeNode(ctx, "us", nil); err != nil {
		t.Fatal(err)
	}

	h.Link(ctx)
	got := clientsByUUID(t, st)
	if id := got["hk"].NodeID; id == nil || *id != hk.ID {
		t.Fatalf("an IPv4 match pairs: %v", id)
	}
	if id := got["jp"].NodeID; id == nil || *id != jp.ID {
		t.Fatalf("an IPv6 match pairs, however it is written: %v", id)
	}
	if got["nat"].NodeID != nil {
		t.Fatal("an address two machines share pairs neither")
	}
	if got["us"].NodeID != nil {
		t.Fatalf("the operator's choice of none stands, though machine %d is at its address", us.ID)
	}
	if got["agent-only"].NodeID != nil {
		t.Fatal("a probe machine with no panel machine stays unpaired")
	}
	if got["hk"].Weight >= got["agent-only"].Weight {
		t.Fatalf("paired machines are listed first on the public page: %d %d", got["hk"].Weight, got["agent-only"].Weight)
	}

	nodes, _ := st.ListNodes(ctx)
	ids := []int64{}
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	ids[0], ids[1] = ids[1], ids[0]
	if err := st.ReorderNodes(ctx, ids); err != nil {
		t.Fatal(err)
	}
	got = clientsByUUID(t, st)
	if got["jp"].Weight >= got["hk"].Weight {
		t.Fatalf("the public page follows the panel's order: jp %d hk %d", got["jp"].Weight, got["hk"].Weight)
	}

	if err := st.SetProbeNode(ctx, "agent-only", &hk.ID); err != nil {
		t.Fatal(err)
	}
	got = clientsByUUID(t, st)
	if id := got["agent-only"].NodeID; id == nil || *id != hk.ID || got["hk"].NodeID != nil {
		t.Fatal("a machine pairs with one probe machine; choosing another moves it")
	}
	missing := int64(9999)
	if err := st.SetProbeNode(ctx, "hk", &missing); err == nil {
		t.Fatal("a machine that does not exist cannot be chosen")
	}
}

// A machine with no saved place yet comes after those with one, on the
// public page as on the panel's machine list.
func TestNewMachinesComeLastOnThePublicPage(t *testing.T) {
	_, st, _ := fixture(t)
	ctx := context.Background()
	if err := st.ReplaceProbe(ctx, store.ProbeImport{Clients: []store.ProbeClient{{UUID: "old", Token: "t1", Name: "Old"}, {UUID: "new", Token: "t2", Name: "New"}}}); err != nil {
		t.Fatal(err)
	}
	a := node(t, st, "zz-placed", "192.0.2.1")
	if err := st.ReorderNodes(ctx, []int64{a.ID}); err != nil {
		t.Fatal(err)
	}
	b := node(t, st, "aa-new", "192.0.2.2")
	if err := st.SetProbeNode(ctx, "old", &a.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProbeNode(ctx, "new", &b.ID); err != nil {
		t.Fatal(err)
	}
	nodes, _ := st.ListNodes(ctx)
	got := clientsByUUID(t, st)
	if nodes[0].ID != a.ID || got["old"].Weight >= got["new"].Weight {
		t.Fatalf("panel order %v, weights old %d new %d", []int64{nodes[0].ID, nodes[1].ID}, got["old"].Weight, got["new"].Weight)
	}
}
