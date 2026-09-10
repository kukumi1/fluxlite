package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/store"
)

func newReinstallService(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fluxlite.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	// applyEnrollment only touches the store; leaving the ssh/applier
	// collaborators nil keeps the test on the decision it is actually about.
	return &Service{store: st}
}

func seedReinstallNode(t *testing.T, s *Service) *model.Node {
	t.Helper()
	n := &model.Node{
		Name:         "香港中转",
		Host:         "203.0.113.9",
		SSHPort:      22,
		SSHUser:      "root",
		AuthType:     model.AuthPassword,
		AuthSecret:   []byte("old-sealed-password"),
		PortStart:    10000,
		PortEnd:      20000,
		HostKey:      "ssh-ed25519 AAAAOLD original-host-key",
		Arch:         "amd64",
		OSID:         "debian",
		InitSystem:   model.InitSystemd,
		RealmVersion: "2.9.4",
	}
	if err := s.store.CreateNode(context.Background(), n); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	return n
}

func reinstallToken(target *int64) *store.EnrollToken {
	return &store.EnrollToken{
		Token: "tok",
		// Deliberately different from the node's own values: a ticket can be
		// minutes old and the operator may have corrected the address since.
		Name:          "券里的旧名字",
		Host:          "198.51.100.1",
		SSHPort:       2222,
		SSHUser:       "someone",
		PortStart:     1,
		PortEnd:       65535,
		PrivateKey:    []byte("new-sealed-key"),
		AuthorizedKey: "ssh-ed25519 AAAANEW fluxlite",
		ExpiresAt:     time.Now().UTC().Add(time.Hour),
		TargetNodeID:  target,
	}
}

var reinstallReport = EnrollReport{
	Arch:         "arm64",
	OSID:         "alpine",
	InitSystem:   "openrc",
	RealmVersion: "2.9.5",
}

// A reinstall must land on the node that already exists. Creating a second one
// is the failure that matters: the original stays unreachable, every route
// keeps pointing at it, and the panel looks like it succeeded.
func TestApplyEnrollmentReusesTargetNodeAndKeepsRoutes(t *testing.T) {
	ctx := context.Background()
	s := newReinstallService(t)
	node := seedReinstallNode(t, s)

	route := &model.Route{
		Name:      "香港转台湾",
		Slug:      "hk-tw",
		Target:    "203.0.113.200:443",
		Protocol:  model.ProtocolTCP,
		Enabled:   true,
		EntryPort: 10001,
		Hops:      []model.RouteHop{{NodeID: node.ID, HopOrder: 0, RelayPort: 10001}},
	}
	if err := s.store.CreateRoute(ctx, route); err != nil {
		t.Fatalf("seed route: %v", err)
	}

	got, reinstalled, err := s.applyEnrollment(ctx, reinstallToken(&node.ID), reinstallReport, model.InitOpenRC)
	if err != nil {
		t.Fatalf("applyEnrollment: %v", err)
	}
	if !reinstalled {
		t.Error("带目标节点的券应当被标记为重装")
	}
	if got.ID != node.ID {
		t.Fatalf("节点 id 变了 (%d -> %d)，链路会全部指向一个坏节点", node.ID, got.ID)
	}

	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("重装不该新增节点，现在有 %d 个", len(nodes))
	}

	routes, err := s.store.RoutesOnNode(ctx, node.ID)
	if err != nil {
		t.Fatalf("routes on node: %v", err)
	}
	if len(routes) != 1 {
		t.Fatalf("链路应当仍挂在这个节点上，实际 %d 条", len(routes))
	}

	fresh, err := s.store.NodeByID(ctx, node.ID)
	if err != nil {
		t.Fatalf("reload node: %v", err)
	}
	// Replaced, because the reinstall invalidated them.
	if fresh.HostKey != "" {
		t.Errorf("主机指纹没清空，重装后的机器会因指纹不符连不上: %q", fresh.HostKey)
	}
	if string(fresh.AuthSecret) != "new-sealed-key" {
		t.Errorf("私钥没换成券里的新私钥: %q", fresh.AuthSecret)
	}
	if fresh.AuthType != model.AuthKey {
		t.Errorf("认证方式应改为密钥，实际 %q", fresh.AuthType)
	}
	if fresh.Arch != "arm64" || fresh.OSID != "alpine" || fresh.RealmVersion != "2.9.5" {
		t.Errorf("系统信息没按新机器刷新: %s/%s realm %s", fresh.OSID, fresh.Arch, fresh.RealmVersion)
	}
	// Kept, because they describe how to reach the machine and the node record
	// is newer than the ticket.
	if fresh.Name != "香港中转" || fresh.Host != "203.0.113.9" || fresh.SSHPort != 22 {
		t.Errorf("券里的旧值覆盖了节点自己的连接信息: %s %s:%d", fresh.Name, fresh.Host, fresh.SSHPort)
	}
	if fresh.PortStart != 10000 || fresh.PortEnd != 20000 {
		t.Errorf("端口池被券覆盖了: %d-%d", fresh.PortStart, fresh.PortEnd)
	}
}

// The ordinary path must stay untouched: a ticket with no target creates a
// node, and nothing about reinstall leaks into it.
func TestApplyEnrollmentCreatesNodeWhenTicketHasNoTarget(t *testing.T) {
	ctx := context.Background()
	s := newReinstallService(t)

	got, reinstalled, err := s.applyEnrollment(ctx, reinstallToken(nil), reinstallReport, model.InitOpenRC)
	if err != nil {
		t.Fatalf("applyEnrollment: %v", err)
	}
	if reinstalled {
		t.Error("没有目标节点的券不该被当成重装")
	}
	if got.ID == 0 {
		t.Fatal("新节点没有拿到 id")
	}
	if got.Host != "198.51.100.1" || got.SSHPort != 2222 {
		t.Errorf("新建节点应当用券里的连接信息，实际 %s:%d", got.Host, got.SSHPort)
	}
}

// A ticket outliving its node must fail loudly. Silently creating a node
// instead would resurrect a machine the operator had deleted on purpose.
func TestApplyEnrollmentFailsWhenTargetNodeIsGone(t *testing.T) {
	ctx := context.Background()
	s := newReinstallService(t)

	missing := int64(4242)
	if _, _, err := s.applyEnrollment(ctx, reinstallToken(&missing), reinstallReport, model.InitSystemd); err == nil {
		t.Fatal("目标节点已不存在时必须报错，而不是新建一个")
	}

	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("失败路径不该留下节点，实际 %d 个", len(nodes))
	}
}
