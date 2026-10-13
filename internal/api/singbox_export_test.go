package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image/png"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/auth"
	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/singbox"
	"github.com/kukumi1/fluxlite/internal/store"
)

func exportHTTPFixture(t *testing.T, passwords ...string) (http.Handler, int64, string) {
	t.Helper()
	srv, st := newAuditTestServer(t)
	ctx := context.Background()
	// Each fixture gets its own account, DB and opaque test session.
	account := store.User{Username: "export-qa-operator", PasswordHash: "not-used-for-login-in-this-test"}
	if err := st.CreateUser(ctx, &account); err != nil {
		t.Fatal(err)
	}
	const token = "isolated-export-test-session"
	if err := st.CreateSession(ctx, token, account.ID, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	srv.auth = auth.New(st)
	sealer, err := cryptox.NewSealer(bytes.Repeat([]byte{9}, cryptox.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	srv.svc = service.New(st, sealer, nil, nil, nil, nil, slog.Default())
	node := &model.Node{Name: "export-test", Host: "203.0.113.11", SSHPort: 22, SSHUser: "root", AuthType: model.AuthPassword, AuthSecret: []byte("test"), PortStart: 21000, PortEnd: 21999}
	if err := st.CreateNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	users := make([]map[string]string, 0, len(passwords))
	for _, password := range passwords {
		users = append(users, map[string]string{"password": password})
	}
	raw, err := json.Marshal(map[string]any{"inbounds": []any{map[string]any{"type": "anytls", "listen": "0.0.0.0", "listen_port": 21200, "users": users, "tls": map[string]any{"enabled": true, "certificate_path": "/server/cert.pem", "key_path": "/server/key.pem"}}}})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := json.Marshal(&singbox.Bundle{Config: raw, Protocol: model.SingBoxAnyTLS, Name: "adopted-test", Port: 21200})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := sealer.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	u := &model.SingBoxUser{NodeID: node.ID, Name: "adopted-test", Protocol: model.SingBoxAnyTLS, Port: 21200, Source: model.SingBoxManaged, Enabled: true, ConfigBlob: blob, ServiceName: "fluxlite-singbox-test", ConfigPath: "/server/config.json", PeriodDays: 30, PeriodStartedAt: now, PeriodEndsAt: now.Add(30 * 24 * time.Hour)}
	if err := st.CreateSingBoxUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	return srv.Handler(), u.ID, token
}

func TestExportAdoptedNodeHTTPHasClientLinkAndQRCode(t *testing.T) {
	h, id, token := exportHTTPFixture(t, "private-qa-node-secret")
	path := "/api/singbox/users/" + strconv.FormatInt(id, 10) + "/export"
	r := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatal("export has no session guard")
	}
	r = httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("export failed: %d", w.Code)
	}
	var result struct {
		Link   string          `json:"link"`
		QR     string          `json:"qr_data_url"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Link, "anytls://") || !strings.Contains(result.Link, "203.0.113.11:21200") {
		t.Fatal("client link missing")
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(result.QR, prefix) {
		t.Fatal("QR PNG missing")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(result.QR, prefix))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil || img.Bounds().Dx() != 256 || img.Bounds().Dy() != 256 {
		t.Fatal("invalid QR PNG", err)
	}
	if strings.Contains(string(result.Config), `"inbounds"`) || strings.Contains(string(result.Config), "/server/") || !strings.Contains(string(result.Config), `"outbounds"`) {
		t.Fatal("server configuration exposed as client JSON")
	}
}

func TestExportAmbiguousNodeHTTPReturnsUsefulError(t *testing.T) {
	h, id, token := exportHTTPFixture(t, "private-one", "private-two")
	r := httptest.NewRequest(http.MethodGet, "/api/singbox/users/"+strconv.FormatInt(id, 10)+"/export", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ambiguous export unexpectedly succeeded: %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "private-one") || strings.Contains(w.Body.String(), "private-two") || strings.Contains(w.Body.String(), `"config"`) {
		t.Fatal("error response exposes server credentials")
	}
}
