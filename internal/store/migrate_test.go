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

func TestAlreadyAppliedOnlyMatchesAddColumn(t *testing.T) {
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
	if _, err := st.db.ExecContext(ctx,
		`UPDATE schema_version SET version = ?`, len(migrations)-1); err != nil {
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
