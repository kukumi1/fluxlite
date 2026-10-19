package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/store"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestSubscriptionLinkHTTP(t *testing.T) {
	srv, st, h, session := recoveryServer(t)
	ctx := context.Background()
	seal, e := cryptox.NewSealer(bytes.Repeat([]byte{3}, 32))
	if e != nil {
		t.Fatal(e)
	}
	srv.svc = service.New(st, seal, nil, nil, nil, nil, nil)
	token := strings.Repeat("C", 43)
	hash := sha256.Sum256([]byte(token))
	secret, e := seal.Seal([]byte(token))
	if e != nil {
		t.Fatal(e)
	}
	v := &store.Subscription{Name: "saved", Enabled: true, Status: "active"}
	if e = st.CreateSubscription(ctx, v, hash[:], secret); e != nil {
		t.Fatal(e)
	}
	path := fmt.Sprintf("/api/subscriptions/%d/link", v.ID)
	if telegramRouteAllowed("GET", path) {
		t.Fatal("TG link allowed")
	}
	if w := recoveryHTTP(t, h, "GET", path, "", "192.0.2.3", nil); w.Code != 401 {
		t.Fatal("anonymous allowed", w.Code)
	}
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer telegram-session")
	denied := httptest.NewRecorder()
	h.ServeHTTP(denied, req)
	if denied.Code != 403 || denied.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("TG link not denied safely", denied.Code)
	}
	for i := 0; i < 2; i++ {
		w := recoveryHTTP(t, h, "GET", path, session, "192.0.2.3", nil)
		var body map[string]string
		json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != 200 || body["token"] != token || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatal(w.Code, w.Body)
		}
	}
	logs, e := st.ListAudit(ctx, 20)
	if e != nil {
		t.Fatal(e)
	}
	views := 0
	for _, entry := range logs {
		data, _ := json.Marshal(entry)
		if bytes.Contains(data, []byte(token)) {
			t.Fatal("audit leaked token")
		}
		if entry.Action == "subscription.link_view" {
			if entry.Target != strconv.FormatInt(v.ID, 10) || entry.Detail != "" {
				t.Fatal("incorrect audit metadata")
			}
			views++
		}
	}
	if views != 2 {
		t.Fatal("view audit missing", views)
	}
	if e = st.ResetSubscriptionToken(ctx, v.ID, hash[:]); e != nil {
		t.Fatal(e)
	}
	w := recoveryHTTP(t, h, "GET", path, session, "192.0.2.3", nil)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "subscription_link_unavailable") {
		t.Fatal(w.Code, w.Body)
	}
	if _, e = st.SubscriptionByHash(ctx, hash[:]); e != nil {
		t.Fatal("legacy hash changed", e)
	}
}
