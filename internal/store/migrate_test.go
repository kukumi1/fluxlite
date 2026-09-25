package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), filepath.Join(t.TempDir(), "fluxlite.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestMigrateCreatesEnrollTokenColumns(t *testing.T) {
	st := openTemp(t)

	tok := &EnrollToken{
		Token:         "t1",
		Name:          "台北落地",
		Host:          "vps.taotao99.xyz",
		SSHPort:       22,
		SSHUser:       "root",
		PortStart:     1,
		PortEnd:       65535,
		SkipUDPProbe:  true,
		PrivateKey:    []byte("key"),
		AuthorizedKey: "ssh-ed25519 AAAA",
		ExpiresAt:     time.Now().Add(time.Hour),
	}
	if err := st.CreateEnrollToken(context.Background(), tok); err != nil {
		t.Fatalf("create enroll token: %v", err)
	}

	got, err := st.EnrollTokenByValue(context.Background(), "t1")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !got.SkipUDPProbe {
		t.Error("skip_udp_probe did not round-trip")
	}
}

// A database whose enroll_tokens table already carries skip_udp_probe — the
// shape produced by installing the release that folded the column into CREATE
// TABLE — must still migrate. Rerunning the ADD COLUMN would abort startup.
func TestMigrateToleratesPreexistingColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fluxlite.db")

	st, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := st.db.ExecContext(context.Background(),
		`ALTER TABLE nodes ADD COLUMN future_flag INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatalf("simulate ahead-of-version column: %v", err)
	}
	if _, err := st.db.ExecContext(context.Background(),
		`UPDATE schema_version SET version = ?`, len(migrations)-1); err != nil {
		t.Fatalf("rewind schema_version: %v", err)
	}
	st.Close()

	migrations = append(migrations,
		`ALTER TABLE nodes ADD COLUMN future_flag INTEGER NOT NULL DEFAULT 0`)
	t.Cleanup(func() { migrations = migrations[:len(migrations)-1] })

	st2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen must tolerate an already-present column: %v", err)
	}
	defer st2.Close()

	var version int
	if err := st2.db.QueryRowContext(context.Background(),
		`SELECT version FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if version != len(migrations) {
		t.Errorf("schema_version = %d, want %d", version, len(migrations))
	}
}

// alreadyApplied 只对「加列」和「删列」判空操作，别的语句一律执行。判错方向的
// 代价不对称：把没生效的当成已生效会静默缺列，把已生效的当成没生效会让 Open 直接
// 失败、面板起不来。
func TestAlreadyAppliedMatchesOnlyColumnChanges(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()

	cases := []struct {
		stmt string
		want bool
	}{
		{`ALTER TABLE nodes ADD COLUMN skip_udp_probe INTEGER NOT NULL DEFAULT 0`, true},
		{`ALTER TABLE nodes ADD COLUMN nonexistent INTEGER`, false},
		{`CREATE TABLE IF NOT EXISTS nodes (id INTEGER)`, false},
		{`UPDATE routes SET slug = name WHERE slug = ''`, false},
		// 删列：列还在 = 这条还没生效；列没了 = 已经生效，重放必须跳过。
		{`ALTER TABLE nodes DROP COLUMN skip_udp_probe`, false},
		{`ALTER TABLE nodes DROP COLUMN listen_ipv6`, true},
	}
	for _, c := range cases {
		got, err := st.alreadyApplied(ctx, c.stmt)
		if err != nil {
			t.Fatalf("alreadyApplied(%q): %v", c.stmt, err)
		}
		if got != c.want {
			t.Errorf("alreadyApplied(%q) = %v, want %v", c.stmt, got, c.want)
		}
	}
}

func TestMigrateIsIdempotentAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fluxlite.db")
	for i := 0; i < 3; i++ {
		st, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		st.Close()
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.QueryRow(`SELECT version FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if version != len(migrations) {
		t.Errorf("schema_version = %d, want %d", version, len(migrations))
	}
}

// 迁移进度是按「已应用条数」记的，所以往列表中间插一条，会让所有已经迁移完
// 的库从下一条开始跑 —— 插进去的那条被整条跳过。新库看不出任何问题，老库
// 静默缺列，正是 avatar 那次的情形。
//
// 这里模拟的就是那种库：版本号已经数满，列却不存在。
func TestMigrateRepairsColumnSkippedByMidListInsert(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fluxlite.db")

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `ALTER TABLE users DROP COLUMN avatar`); err != nil {
		t.Fatalf("模拟缺列失败: %v", err)
	}
	// 版本要停在「那条修补迁移之前」。不能写 len(migrations)-1 —— 之后每追加
	// 一条迁移，这个假设就失效一次（追加 enroll_tokens.target_node_id 时就是
	// 这么挂的）。这里按内容定位，列表怎么长都不影响。
	repair := -1
	for i, m := range migrations {
		if strings.Contains(m, "ALTER TABLE users ADD COLUMN avatar") {
			repair = i // 取最后一次出现的那条，也就是修补用的那条
		}
	}
	if repair < 0 {
		t.Fatal("迁移列表里找不到 avatar 修补条目，测试已与实现脱节")
	}
	if _, err := st.db.ExecContext(ctx,
		`UPDATE schema_version SET version = ?`, repair); err != nil {
		t.Fatalf("回退版本号失败: %v", err)
	}
	st.Close()

	again, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("重新打开（本该自动补上缺列）: %v", err)
	}
	defer again.Close()

	has, err := again.hasColumn(ctx, "users", "avatar")
	if err != nil {
		t.Fatalf("检查列: %v", err)
	}
	if !has {
		t.Fatal("重新打开之后 users.avatar 仍然不存在，老库永远用不了头像")
	}

	// 补上之后要能正常读写，而不只是列存在。
	u := &User{Username: "someone", PasswordHash: "x"}
	if err := again.CreateUser(ctx, u); err != nil {
		t.Fatalf("建用户: %v", err)
	}
	if err := again.SetUserAvatar(ctx, u.ID, []byte("not-a-real-png")); err != nil {
		t.Fatalf("写头像: %v", err)
	}
	got, err := again.UserAvatar(ctx, u.ID)
	if err != nil {
		t.Fatalf("读头像: %v", err)
	}
	if string(got) != "not-a-real-png" {
		t.Fatalf("读回来的头像不对: %q", got)
	}
}

// indexOfMigration 返回给某张表加某一列的那条迁移的下标。
//
// 迁移是按条数记录进度的，所以「重放第 i 条」只能靠把版本号退到 i。用下标字面量
// 去指代某条迁移，会在有人往列表里增删时静默指向另一条 —— 测试照样通过，守的却
// 不再是原来那件事。
func indexOfMigration(t *testing.T, table, column string) int {
	t.Helper()

	want := "ALTER TABLE " + table + " ADD COLUMN " + column
	for i, m := range migrations {
		if strings.Contains(m, want) {
			return i
		}
	}
	t.Fatalf("迁移列表里找不到给 %s 加 %s 的那条，测试要守的东西已经不存在了", table, column)
	return -1
}

// indexOfDropMigration 是上面那个的 DROP 版本。
func indexOfDropMigration(t *testing.T, table, column string) int {
	t.Helper()

	want := "ALTER TABLE " + table + " DROP COLUMN " + column
	for i, m := range migrations {
		if strings.Contains(m, want) {
			return i
		}
	}
	t.Fatalf("迁移列表里找不到从 %s 删掉 %s 的那条", table, column)
	return -1
}

// 追加列的迁移在老库上必须真的跑到。avatar 那次就是因为插在列表中间，
// 已迁移完的库整条跳过、线上静默缺列，所以每加一列都值得守一次。
func TestMigrateAddsEnrollTargetNodeColumnToExistingDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "fluxlite.db")

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := st.db.ExecContext(ctx,
		`ALTER TABLE enroll_tokens DROP COLUMN target_node_id`); err != nil {
		t.Fatalf("模拟老库缺列失败: %v", err)
	}
	// 按内容定位，不用 len(migrations)-1。那种写法只在这条迁移恰好排最后时成立，
	// 之后任何人往列表末尾追加一条，这个测试就会悄悄改成在考别的迁移。
	target := indexOfMigration(t, "enroll_tokens", "target_node_id")
	if _, err := st.db.ExecContext(ctx,
		`UPDATE schema_version SET version = ?`, target); err != nil {
		t.Fatalf("回退版本号失败: %v", err)
	}
	st.Close()

	again, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("重新打开（本该补上缺列）: %v", err)
	}
	defer again.Close()

	has, err := again.hasColumn(ctx, "enroll_tokens", "target_node_id")
	if err != nil {
		t.Fatalf("检查列: %v", err)
	}
	if !has {
		t.Fatal("老库没补上 enroll_tokens.target_node_id，重装功能在老库上会直接报错")
	}
}

// 券里带了目标节点就必须能存能取。这个字段错了不会报错，只会让重装悄悄
// 变成「新建了第二个节点」，而原节点连同链路仍然是坏的。
func TestEnrollTokenRoundTripsTargetNode(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)

	target := int64(7)
	tok := &EnrollToken{
		Token:         "reinstall-1",
		Name:          "香港中转",
		Host:          "203.0.113.9",
		SSHPort:       22,
		SSHUser:       "root",
		PortStart:     10000,
		PortEnd:       20000,
		PrivateKey:    []byte("sealed"),
		AuthorizedKey: "ssh-ed25519 AAAA fluxlite",
		ExpiresAt:     time.Now().UTC().Add(time.Hour),
		TargetNodeID:  &target,
	}
	if err := st.CreateEnrollToken(ctx, tok); err != nil {
		t.Fatalf("建券: %v", err)
	}

	got, err := st.EnrollTokenByValue(ctx, "reinstall-1")
	if err != nil {
		t.Fatalf("读券: %v", err)
	}
	if got.TargetNodeID == nil || *got.TargetNodeID != target {
		t.Fatalf("目标节点没有存下来: %v", got.TargetNodeID)
	}

	// 普通注册的券必须仍然是 nil，否则会误伤已有节点。
	plain := *tok
	plain.Token = "fresh-1"
	plain.TargetNodeID = nil
	if err := st.CreateEnrollToken(ctx, &plain); err != nil {
		t.Fatalf("建普通券: %v", err)
	}
	back, err := st.EnrollTokenByValue(ctx, "fresh-1")
	if err != nil {
		t.Fatalf("读普通券: %v", err)
	}
	if back.TargetNodeID != nil {
		t.Fatalf("普通注册的券不该带目标节点，实际 %v", *back.TargetNodeID)
	}
}
