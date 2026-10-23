package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// 生产库在撤回流量审计时已经记到第 62 条迁移。代码里的条数一旦少于它，之后
// 新加的迁移在生产上会整条被跳过：循环从 62 开始，根本走不到它们。
const productionSchemaVersion = 62

func TestMigrationsCoverProductionSchemaVersion(t *testing.T) {
	if len(migrations) < productionSchemaVersion {
		t.Fatalf("代码只有 %d 条迁移，生产库已在 %d。撤回功能时要把它的迁移换成 retiredMigration，不能删",
			len(migrations), productionSchemaVersion)
	}
}

// 新增迁移必须真的在一个停在 62 的库上执行，这正是撤回审计后差点丢掉的路径。
func TestMigrationAfterRetiredSlotsRunsOnProductionVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fluxlite.db")

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("建库: %v", err)
	}
	st.Close()

	full := migrations
	t.Cleanup(func() { migrations = full })
	migrations = append(append([]string(nil), full...), `CREATE TABLE added_later (id INTEGER)`)

	again, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("追加迁移后打开: %v", err)
	}
	defer again.Close()

	var n int
	if err := again.db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE name = 'added_later'`).Scan(&n); err != nil {
		t.Fatalf("查表: %v", err)
	}
	if n != 1 {
		t.Error("排在占位之后的新迁移没有执行")
	}
}

// 库的版本号比代码认识的迁移还多，说明有已上线的迁移被删了。此时必须拒绝启动，
// 否则会静默跳过之后所有新迁移。
func TestMigrateRefusesDatabaseAheadOfBuild(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fluxlite.db")

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("建库: %v", err)
	}
	st.Close()

	full := migrations
	t.Cleanup(func() { migrations = full })
	migrations = full[:len(full)-1]

	again, err := Open(ctx, path)
	if err == nil {
		again.Close()
		t.Fatal("库的版本号超过代码的迁移条数，Open 却成功了")
	}
	if !strings.Contains(err.Error(), "newer than") {
		t.Errorf("报错没说清原因: %v", err)
	}
}
