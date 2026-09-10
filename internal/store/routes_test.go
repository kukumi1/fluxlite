package store

import (
	"context"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
)

// RoutesOnNode 只在「节点确实有链路」时才会执行 Scan，所以列数写错时零链路的
// 节点一切正常、有链路的节点直接报 SQL 错。而调用它的正是 DeleteNode 的占用
// 检查 —— 本该给出「被 N 条链路占用」，实际给出的是 Scan 参数个数不符。
func TestRoutesOnNodeReturnsRoutesInsteadOfScanError(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)

	node := &model.Node{
		Name: "测试节点", Host: "203.0.113.5", SSHPort: 22, SSHUser: "root",
		AuthType: model.AuthKey, AuthSecret: []byte("k"),
		PortStart: 10000, PortEnd: 20000,
	}
	if err := st.CreateNode(ctx, node); err != nil {
		t.Fatalf("建节点: %v", err)
	}

	// 零链路时即使列数写错也查得动，所以先确认这个基线，再加链路。
	if got, err := st.RoutesOnNode(ctx, node.ID); err != nil || len(got) != 0 {
		t.Fatalf("没有链路时应当返回空且无错，实际 %v / %v", got, err)
	}

	route := &model.Route{
		Name: "占用它的链路", Slug: "in-use", Target: "203.0.113.9:443",
		Protocol: model.ProtocolTCP, Enabled: true, EntryPort: 10001,
		QuotaBytes: func() *int64 { v := int64(1 << 30); return &v }(),
		Hops:       []model.RouteHop{{NodeID: node.ID, HopOrder: 0, RelayPort: 10001}},
	}
	if err := st.CreateRoute(ctx, route); err != nil {
		t.Fatalf("建链路: %v", err)
	}

	got, err := st.RoutesOnNode(ctx, node.ID)
	if err != nil {
		t.Fatalf("有链路时查询失败（列数与 scanRoute 不一致就会这样）: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("应当查到 1 条链路，实际 %d 条", len(got))
	}
	if got[0].Name != "占用它的链路" {
		t.Errorf("查到的链路不对: %q", got[0].Name)
	}
	// 配额列是当初漏掉的那三列，读回来必须是真值而不是零值。
	if got[0].QuotaBytes == nil || *got[0].QuotaBytes != 1<<30 {
		t.Errorf("配额没读出来: %v", got[0].QuotaBytes)
	}
}
