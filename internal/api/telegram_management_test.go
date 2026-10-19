package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

// Exercise real business handlers with the restricted identity, not just the
// route predicate. No SSH client is configured: this fixture never contacts a host.
func TestTelegramMachineManagementAndAudit(t *testing.T) {
	s := telegramPollFixture(t)
	owner, err := s.svc.Store().UserByID(context.Background(), s.telegram.cfg.UserID)
	if err != nil {
		t.Fatal(err)
	}
	token := "test-only-management-session"
	s.telegram.sessions[token] = telegramSession{UserID: owner.ID, Actor: 42, Expires: time.Now().Add(time.Minute), Credentials: telegramCredentialsFingerprint(owner)}
	h := s.Handler()
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	secret := "synthetic-ssh-password"
	w := call("POST", "/api/nodes", map[string]any{"name": "TG 测试机器", "host": "192.0.2.25", "ssh_port": 22, "ssh_user": "root", "auth_type": "password", "secret": secret, "port_start": 20000, "port_end": 20100})
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("SSH credential leaked in response")
	}
	var node model.Node
	if err := json.Unmarshal(w.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	if node.ID <= 0 {
		t.Fatal("missing machine id")
	}
	if w = call("GET", "/api/nodes", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "TG 测试机器") {
		t.Fatal("machine not visible")
	}
	if w = call("DELETE", fmt.Sprintf("/api/nodes/%d", node.ID), nil); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	logs, err := s.svc.Store().ListAudit(context.Background(), 10)
	if err != nil || len(logs) != 2 {
		t.Fatalf("audit: %v %v", logs, err)
	}
	for _, entry := range logs {
		if entry.Actor != "telegram:42:operator" || strings.Contains(entry.Detail, secret) {
			t.Fatalf("invalid audit: %+v", entry)
		}
	}
	// Authentication must fail even when the URL itself is in the allowlist.
	session := s.telegram.sessions[token]
	session.Expires = time.Now().Add(-time.Second)
	s.telegram.sessions[token] = session
	if w = call("GET", "/api/nodes", nil); w.Code != 401 {
		t.Fatal("expired session accepted")
	}
	session.Expires = time.Now().Add(time.Minute)
	s.telegram.sessions[token] = session
	if err := s.svc.Store().SetUserPassword(context.Background(), owner.ID, "changed-credential-fingerprint"); err != nil {
		t.Fatal(err)
	}
	if w = call("GET", "/api/nodes", nil); w.Code != 401 {
		t.Fatal("session survived password change")
	}
}
