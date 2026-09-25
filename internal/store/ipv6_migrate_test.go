package store

import (
	"context"
	"path/filepath"
	"testing"
)

// IPv6 入口开关从节点挪到了链路。迁移要把两件事都做对：链路表补上列，节点表
// 去掉那列。漏掉任何一半，面板启动时扫描的列和表里的列就对不上。
func TestMigrateMovesListenIPv6FromNodeToRoute(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)

	hasRoute, err := st.hasColumn(ctx, "routes", "listen_ipv6")
	if err != nil {
		t.Fatalf("检查 routes.listen_ipv6: %v", err)
	}
	if !hasRoute {
		t.Error("routes 没有 listen_ipv6，按链路开关无处存放")
	}

	hasNode, err := st.hasColumn(ctx, "nodes", "listen_ipv6")
	if err != nil {
		t.Fatalf("检查 nodes.listen_ipv6: %v", err)
	}
	if hasNode {
		t.Error("nodes.listen_ipv6 没被删掉，留着会让人以为节点级开关还有效")
	}

	hasAddr, err := st.hasColumn(ctx, "nodes", "ipv6_address")
	if err != nil {
		t.Fatalf("检查 nodes.ipv6_address: %v", err)
	}
	if !hasAddr {
		t.Error("nodes 没有 ipv6_address，面板就只能显示 v4 入口地址")
	}
}

// DROP COLUMN 和 ADD COLUMN 一样没有 IF EXISTS。重放一条已经生效的迁移必须是
// 空操作，否则版本号一旦错位，Open 会直接失败、面板起不来。
func TestMigrateToleratesReplayingADroppedColumn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fluxlite.db")

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	drop := indexOfDropMigration(t, "nodes", "listen_ipv6")
	if _, err := st.db.ExecContext(ctx,
		`UPDATE schema_version SET version = ?`, drop); err != nil {
		t.Fatalf("回退版本号失败: %v", err)
	}
	st.Close()

	// 列已经没了，这一轮会再次走到那条 DROP。它必须被识别为已生效而跳过。
	again, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("重放已生效的 DROP COLUMN 时 Open 失败，面板会起不来: %v", err)
	}
	defer again.Close()

	has, err := again.hasColumn(ctx, "nodes", "listen_ipv6")
	if err != nil {
		t.Fatalf("检查列: %v", err)
	}
	if has {
		t.Error("列又回来了，迁移之间在互相打架")
	}
}
