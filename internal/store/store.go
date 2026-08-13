// Package store persists nodes, routes, operator accounts and audit records
// in a single SQLite file.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflicts with an existing record")
)

// Store owns the database handle and exposes typed accessors.
type Store struct {
	db *sql.DB
}

// Open prepares the database at path, creating and migrating it as needed.
func Open(ctx context.Context, path string) (*Store, error) {
	// busy_timeout keeps concurrent writers from failing outright; foreign_keys
	// is off by default in SQLite and must be enabled per connection.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// SQLite tolerates one writer; a larger pool only produces lock contention.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	// SQLite creates the file with the process umask, which commonly yields
	// 0644. The rows hold encrypted node credentials and session tokens, so
	// the file is restricted to its owner regardless of umask. WAL and shared
	// memory sidecars carry the same data and need the same treatment.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o600); err != nil && !os.IsNotExist(err) {
			db.Close()
			return nil, fmt.Errorf("restrict database permissions: %w", err)
		}
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for callers that need a transaction.
func (s *Store) DB() *sql.DB { return s.db }

// migrations are applied in order; each runs exactly once. Never edit a
// migration that has shipped, append a new one instead.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS nodes (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		name          TEXT    NOT NULL UNIQUE,
		host          TEXT    NOT NULL,
		ssh_port      INTEGER NOT NULL,
		ssh_user      TEXT    NOT NULL,
		auth_type     TEXT    NOT NULL,
		auth_secret   BLOB    NOT NULL,
		via_node_id   INTEGER REFERENCES nodes(id) ON DELETE RESTRICT,
		port_start    INTEGER NOT NULL,
		port_end      INTEGER NOT NULL,
		host_key      TEXT    NOT NULL DEFAULT '',
		arch          TEXT    NOT NULL DEFAULT '',
		os_id         TEXT    NOT NULL DEFAULT '',
		init_system   TEXT    NOT NULL DEFAULT '',
		udp_capable   INTEGER,
		realm_version TEXT    NOT NULL DEFAULT '',
		status        TEXT    NOT NULL DEFAULT 'unknown',
		last_seen     DATETIME,
		created_at    DATETIME NOT NULL,
		updated_at    DATETIME NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS routes (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT    NOT NULL UNIQUE,
		target     TEXT    NOT NULL,
		protocol   TEXT    NOT NULL,
		enabled    INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS route_hops (
		route_id   INTEGER NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
		hop_order  INTEGER NOT NULL,
		node_id    INTEGER NOT NULL REFERENCES nodes(id) ON DELETE RESTRICT,
		relay_port INTEGER NOT NULL,
		PRIMARY KEY (route_id, hop_order)
	)`,

	// One listener per port per node. This is what stops two routes from
	// silently claiming the same port on the same machine.
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_route_hops_node_port
		ON route_hops(node_id, relay_port)`,

	`CREATE INDEX IF NOT EXISTS idx_route_hops_node ON route_hops(node_id)`,

	`CREATE TABLE IF NOT EXISTS users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		username      TEXT    NOT NULL UNIQUE,
		password_hash TEXT    NOT NULL,
		totp_secret   TEXT    NOT NULL DEFAULT '',
		totp_enrolled INTEGER NOT NULL DEFAULT 0,
		failed_count  INTEGER NOT NULL DEFAULT 0,
		locked_until  DATETIME,
		created_at    DATETIME NOT NULL,
		updated_at    DATETIME NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS sessions (
		token      TEXT PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at DATETIME NOT NULL,
		created_at DATETIME NOT NULL
	)`,

	`CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at)`,

	`CREATE TABLE IF NOT EXISTS audit_logs (
		id     INTEGER PRIMARY KEY AUTOINCREMENT,
		ts     DATETIME NOT NULL,
		actor  TEXT NOT NULL,
		action TEXT NOT NULL,
		target TEXT NOT NULL DEFAULT '',
		detail TEXT NOT NULL DEFAULT '',
		ip     TEXT NOT NULL DEFAULT ''
	)`,

	`CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_logs(ts DESC)`,

	// Enrollment tokens let an operator paste one command onto a new machine
	// instead of typing its address and credentials into the panel. The node's
	// private key is generated here and never leaves the panel; the script
	// only ever receives the public half.
	`CREATE TABLE IF NOT EXISTS enroll_tokens (
		token          TEXT PRIMARY KEY,
		name           TEXT    NOT NULL,
		host           TEXT    NOT NULL,
		ssh_port       INTEGER NOT NULL,
		ssh_user       TEXT    NOT NULL,
		port_start     INTEGER NOT NULL,
		port_end       INTEGER NOT NULL,
		via_node_id    INTEGER REFERENCES nodes(id) ON DELETE SET NULL,
		private_key    BLOB    NOT NULL,
		authorized_key TEXT    NOT NULL,
		expires_at     DATETIME NOT NULL,
		used_at        DATETIME,
		node_id        INTEGER REFERENCES nodes(id) ON DELETE SET NULL,
		created_at     DATETIME NOT NULL
	)`,

	`CREATE INDEX IF NOT EXISTS idx_enroll_expiry ON enroll_tokens(expires_at)`,

	`ALTER TABLE nodes ADD COLUMN skip_udp_probe INTEGER NOT NULL DEFAULT 0`,

	// Display names became free-form (Chinese, punctuation), so the identifier
	// that reaches systemd and the filesystem is split out. Existing routes
	// keep their current name as slug: their deployed unit names must not
	// change underneath them.
	`ALTER TABLE routes ADD COLUMN slug TEXT NOT NULL DEFAULT ''`,
	`UPDATE routes SET slug = name WHERE slug = ''`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_routes_slug ON routes(slug)`,

	`ALTER TABLE enroll_tokens ADD COLUMN skip_udp_probe INTEGER NOT NULL DEFAULT 0`,

	// Verification measures each hop's reach to what it forwards to. Keeping
	// the result lets the route list show the shape of the chain's latency
	// without re-running a probe on every page load.
	`ALTER TABLE route_hops ADD COLUMN latency_ms INTEGER`,
	`ALTER TABLE route_hops ADD COLUMN latency_at DATETIME`,

	// Liveness is sampled in the background alongside latency. Reading it from
	// here keeps the status endpoint a database query; asking the nodes on
	// every page load meant an SSH session per hop, which is why the route
	// list could not poll it.
	`ALTER TABLE route_hops ADD COLUMN running INTEGER`,
	`ALTER TABLE route_hops ADD COLUMN checked_at DATETIME`,

	// Byte counters live in the kernel and reset on reboot or a firewall
	// flush, so the panel keeps its own running total. raw_in/raw_out hold the
	// last reading the delta was computed against; NULL means no baseline has
	// been taken yet, which is not the same as a counter sitting at zero.
	`CREATE TABLE IF NOT EXISTS route_traffic (
		route_id   INTEGER NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
		hop_order  INTEGER NOT NULL,
		bytes_in   INTEGER NOT NULL DEFAULT 0,
		bytes_out  INTEGER NOT NULL DEFAULT 0,
		raw_in     INTEGER,
		raw_out    INTEGER,
		updated_at DATETIME,
		PRIMARY KEY (route_id, hop_order)
	)`,

	// Daily buckets are the entry hop's traffic only: every byte the route
	// carries enters there, and summing all hops would count each byte once
	// per machine it passed through.
	`CREATE TABLE IF NOT EXISTS route_traffic_daily (
		route_id  INTEGER NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
		day       TEXT    NOT NULL,
		bytes_in  INTEGER NOT NULL DEFAULT 0,
		bytes_out INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (route_id, day)
	)`,
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	var current int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&current)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (0)`); err != nil {
			return fmt.Errorf("seed schema_version: %w", err)
		}
		current = 0
	case err != nil:
		return fmt.Errorf("read schema_version: %w", err)
	}

	for i := current; i < len(migrations); i++ {
		done, err := s.alreadyApplied(ctx, migrations[i])
		if err != nil {
			return err
		}
		if !done {
			if _, err := s.db.ExecContext(ctx, migrations[i]); err != nil {
				return fmt.Errorf("apply migration %d: %w", i+1, err)
			}
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE schema_version SET version = ?`, i+1); err != nil {
			return fmt.Errorf("record migration %d: %w", i+1, err)
		}
	}
	return nil
}

var addColumnPattern = regexp.MustCompile(`(?i)^\s*ALTER\s+TABLE\s+(\w+)\s+ADD\s+COLUMN\s+(\w+)`)

// alreadyApplied reports whether a migration's effect is present regardless of
// what schema_version claims.
//
// Only ADD COLUMN can legitimately be a no-op. SQLite has no ADD COLUMN IF NOT
// EXISTS, so a database created from a CREATE TABLE that already lists the
// column would fail here on every start: its schema_version says the migration
// is pending while its schema says it is done. Every other statement must
// apply cleanly, so an unrecognised one is always executed.
func (s *Store) alreadyApplied(ctx context.Context, stmt string) (bool, error) {
	m := addColumnPattern.FindStringSubmatch(stmt)
	if m == nil {
		return false, nil
	}
	return s.hasColumn(ctx, m[1], m[2])
}

func (s *Store) hasColumn(ctx context.Context, table, column string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n); err != nil {
		return false, fmt.Errorf("inspect %s.%s: %w", table, column, err)
	}
	return n > 0, nil
}

// inTx runs fn inside a transaction, rolling back on error.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		// The rollback error is deliberately discarded: the caller needs the
		// original failure, not the cleanup outcome.
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
