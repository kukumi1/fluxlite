package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/store"
)

func newAuditTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fluxlite.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	// Only the store and the logger are reachable from audit(); the remaining
	// dependencies are left nil so that a change routing audit through any of
	// them fails here rather than in production.
	svc := service.New(st, nil, nil, nil, nil, nil, slog.Default())
	return NewServer(Config{Service: svc, Logger: slog.Default()}), st
}

// audit() runs after the action it describes has already taken effect, so a
// client that walks away must not be able to erase the record of it. This bit
// the panel for real: a reinstall report that a reverse proxy timed out lost
// the entry saying the node's pinned host key had been reset — the one action
// on that path most worth having written down.
func TestAuditSurvivesCancelledRequest(t *testing.T) {
	srv, st := newAuditTestServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodPost, "/api/enroll/report", nil).WithContext(ctx)
	cancel()

	srv.audit(r, "enroll.reinstall_complete", "CDT-HK", "主机指纹已重置")

	entries, err := st.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("请求被取消后审计记录丢了，应有 1 条，实际 %d 条", len(entries))
	}
	if entries[0].Action != "enroll.reinstall_complete" {
		t.Errorf("记下的动作不对: %q", entries[0].Action)
	}
	if entries[0].Target != "CDT-HK" {
		t.Errorf("记下的目标不对: %q", entries[0].Target)
	}
}

func TestAuditRecordsNormalRequest(t *testing.T) {
	srv, st := newAuditTestServer(t)

	r := httptest.NewRequest(http.MethodPost, "/api/nodes", nil)
	srv.audit(r, "node.create", "Lam-JP", "")

	entries, err := st.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("应有 1 条审计记录，实际 %d 条", len(entries))
	}
	if entries[0].Actor != "anonymous" {
		t.Errorf("没有登录用户时应记为 anonymous，实际 %q", entries[0].Actor)
	}
}
