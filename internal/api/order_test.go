package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

func TestOrderHTTPAuthenticationValidationAndAudit(t *testing.T) {
	srv, st, h, session := recoveryServer(t)
	ctx := context.Background()
	n := &model.Node{Name: "node", Host: "192.0.2.1", SSHPort: 22, SSHUser: "root", AuthType: model.AuthKey, AuthSecret: []byte("secret"), PortStart: 10000, PortEnd: 20000}
	if err := st.CreateNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	r := &model.Route{Name: "route", Slug: "route", Target: "192.0.2.2:443", Protocol: model.ProtocolTCP, Enabled: true, EntryPort: 10001, Hops: []model.RouteHop{{NodeID: n.ID, HopOrder: 0, RelayPort: 10001}}}
	if err := st.CreateRoute(ctx, r); err != nil {
		t.Fatal(err)
	}
	u, err := st.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	srv.telegram.cfg = telegramConfig{Enabled: true, UserID: u.ID, BoundUserID: 123}
	srv.telegram.sessions["order-session"] = telegramSession{UserID: u.ID, Actor: 123, Expires: time.Now().Add(time.Hour), Credentials: telegramCredentialsFingerprint(u)}
	for _, tc := range []struct {
		path   string
		id     int64
		action string
	}{{"/api/nodes/order", n.ID, "nodes.reorder"}, {"/api/routes/order", r.ID, "routes.reorder"}} {
		t.Run(tc.action, func(t *testing.T) {
			body := map[string]any{"ids": []int64{tc.id}}
			if w := orderHTTP(t, h, "PUT", tc.path, "", "192.0.2.1", body); w.Code != 401 {
				t.Fatal("anonymous allowed", w.Code)
			}
			for _, bad := range []struct {
				body   any
				status int
			}{
				{map[string]any{"ids": []int64{tc.id, tc.id}}, 400}, {map[string]any{"ids": []int64{0}}, 400},
				{map[string]any{"ids": []int64{}}, 409}, {map[string]any{"ids": []int64{9999}}, 409},
				{map[string]any{}, 400}, {map[string]any{"ids": nil}, 400},
			} {
				if w := orderHTTP(t, h, "PUT", tc.path, session, "192.0.2.1", bad.body); w.Code != bad.status {
					t.Fatalf("got %d want %d: %s", w.Code, bad.status, w.Body)
				}
			}
			if w := orderHTTP(t, h, "PUT", tc.path, session, "192.0.2.1", body); w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
			req := httptest.NewRequest("PUT", tc.path, strings.NewReader(`{"ids":[1]}`))
			req.Header.Set("Authorization", "Bearer order-session")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatal("TG reorder failed", w.Code, w.Body)
			}
			for _, method := range []string{"GET", "POST", "DELETE", "PATCH"} {
				if telegramRouteAllowed(method, tc.path) {
					t.Fatal("unexpected TG method", method)
				}
			}
		})
	}
	for _, path := range []string{"/api/account/password", "/api/nodes/1/console", "/api/console/unlock", "/api/nodes/order/console"} {
		for _, method := range []string{"GET", "PUT", "POST", "DELETE"} {
			if telegramRouteAllowed(method, path) {
				t.Fatal("sensitive TG path allowed", path)
			}
			req := httptest.NewRequest(method, path, nil)
			req.Header.Set("Authorization", "Bearer order-session")
			w := httptest.NewRecorder()
			srv.authenticateTelegram(w, req, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("restricted handler reached") }))
			if w.Code != http.StatusForbidden {
				t.Fatal("sensitive TG request not denied", path, w.Code)
			}
		}
	}
	logs, err := st.ListAudit(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, entry := range logs {
		if entry.Action == "nodes.reorder" || entry.Action == "routes.reorder" {
			counts[entry.Action]++
			if entry.Detail != "" || entry.Target != "" {
				t.Fatal("unexpected audit metadata")
			}
		}
	}
	if counts["nodes.reorder"] != 2 || counts["routes.reorder"] != 2 {
		t.Fatal("missing or failed-request audits", counts)
	}
}

func orderHTTP(t *testing.T, h http.Handler, method, path, token, ip string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.RemoteAddr = ip + ":12345"
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
