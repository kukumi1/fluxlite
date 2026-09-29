package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
)

func routeNamed(nodeID int64, name, slug string, port int, v6 bool) *model.Route {
	return &model.Route{
		Name: name, Slug: slug, Target: "203.0.113.9:443",
		Protocol: model.ProtocolTCP, Enabled: true, ListenIPv6: v6, EntryPort: port,
		Hops: []model.RouteHop{{NodeID: nodeID, HopOrder: 0, RelayPort: port}},
	}
}

func seedNode(t *testing.T, st *Store) *model.Node {
	t.Helper()
	node := &model.Node{
		Name: "入口", Host: "203.0.113.5", SSHPort: 22, SSHUser: "root",
		AuthType: model.AuthKey, AuthSecret: []byte("k"),
		PortStart: 10000, PortEnd: 20000,
	}
	if err := st.CreateNode(context.Background(), node); err != nil {
		t.Fatalf("建节点: %v", err)
	}
	return node
}

// v4 和 v6 是两套各自列出、各自统计的链路，名称只需在同一套里唯一。
func TestRouteNameIsUniquePerFamily(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	node := seedNode(t, st)

	if err := st.CreateRoute(ctx, routeNamed(node.ID, "Zouter-FAC-TW", "a", 10001, false)); err != nil {
		t.Fatalf("建 v4 链路: %v", err)
	}
	if err := st.CreateRoute(ctx, routeNamed(node.ID, "Zouter-FAC-TW", "b", 10002, true)); err != nil {
		t.Fatalf("v6 转发用了和 v4 相同的名称就被拒，两套本应互不相干: %v", err)
	}

	err := st.CreateRoute(ctx, routeNamed(node.ID, "Zouter-FAC-TW", "c", 10003, true))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("同一套里重名应当报冲突，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "IPv6") || strings.Contains(err.Error(), "existing record") {
		t.Errorf("报错要说清是哪一套里重名，且不带英文尾巴: %q", err.Error())
	}

	// 把 v4 那条切成 v6，会和已有的 v6 同名链路撞上，同样要被拦住。
	routes, err := st.ListRoutes(ctx)
	if err != nil {
		t.Fatalf("列链路: %v", err)
	}
	for _, r := range routes {
		if r.Slug == "a" {
			r.ListenIPv6 = true
			if err := st.UpdateRoute(ctx, r); !errors.Is(err, ErrConflict) {
				t.Errorf("改成 v6 后与已有 v6 链路重名，应当报冲突，实际 %v", err)
			}
		}
	}
}

// 放开名称唯一要重建 routes 表。子表都挂着 ON DELETE CASCADE，重建时外键若是
// 开着，DROP TABLE 会把每条链路的跳和流量一并删光 —— 升级后链路还在，路径和
// 累计流量却没了。所以要用升级前就有数据的库来验证。
func TestRebuildingRoutesKeepsHopsAndTraffic(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fluxlite.db")

	full := migrations
	t.Cleanup(func() { migrations = full })
	migrations = full[:len(full)-1]

	old, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("按升级前的结构建库: %v", err)
	}
	node := seedNode(t, old)
	route := routeNamed(node.ID, "老链路", "old", 10001, false)
	if err := old.CreateRoute(ctx, route); err != nil {
		t.Fatalf("建链路: %v", err)
	}
	record(t, old, route.ID, 100, 200)
	record(t, old, route.ID, 1100, 2200)
	old.Close()

	migrations = full
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("升级失败: %v", err)
	}
	defer st.Close()

	got, err := st.RouteByID(ctx, route.ID)
	if err != nil {
		t.Fatalf("升级后链路丢了: %v", err)
	}
	if len(got.Hops) != 1 || got.Hops[0].RelayPort != 10001 {
		t.Errorf("升级后跳没了或变了: %+v", got.Hops)
	}
	if tr := trafficOf(t, st, route.ID); tr.BytesIn != 1000 || tr.BytesOut != 2000 {
		t.Errorf("升级后累计流量变了: %+v", tr)
	}

	var fk int
	if err := st.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("读外键开关: %v", err)
	}
	if fk != 1 {
		t.Error("迁移完外键没重新打开，之后删链路不会级联清掉跳和流量")
	}

	if err := st.CreateRoute(ctx, routeNamed(node.ID, "老链路", "new", 10002, true)); err != nil {
		t.Errorf("升级后的库仍然不许 v6 与 v4 同名: %v", err)
	}
	if err := st.CreateRoute(ctx, routeNamed(node.ID, "别的", "old", 10003, false)); !errors.Is(err, ErrConflict) {
		t.Errorf("重建后 slug 唯一索引丢了，两条链路会共用同一个服务名: %v", err)
	}
}
