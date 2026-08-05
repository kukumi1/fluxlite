package store

import (
	"context"
	"database/sql"
	"path/filepath"
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
