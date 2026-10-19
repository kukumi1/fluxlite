package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/service"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCollectionHTTPContract(t *testing.T) {
	srv, st, _, cookie := recoveryServer(t)
	ctx := context.Background()
	seal, e := cryptox.NewSealer(bytes.Repeat([]byte{3}, 32))
	if e != nil {
		t.Fatal(e)
	}
	srv.svc = service.New(st, seal, nil, nil, nil, nil, nil)
	h := srv.Handler()
	n := &model.Node{Name: "machine", Host: "192.0.2.10", SSHPort: 22, SSHUser: "root", AuthType: model.AuthPassword, AuthSecret: []byte("private-machine-secret"), PortStart: 10000, PortEnd: 11000}
	if e = st.CreateNode(ctx, n); e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	u := &model.SingBoxUser{NodeID: n.ID, Name: "disabled", Port: 10001, Protocol: model.SingBoxSS2022, Source: model.SingBoxManaged, Enabled: false, Status: model.SingBoxStatusStopped, ServiceName: "test", ConfigPath: "test", ConfigBlob: []byte("private-node-secret"), PeriodStartedAt: now, PeriodEndsAt: now.Add(time.Hour), PeriodDays: 30}
	if e = st.CreateSingBoxUser(ctx, u); e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/subscriptions/candidates", nil))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	w = recoveryHTTP(t, h, "GET", "/api/subscriptions/candidates", cookie, "192.0.2.3", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"subscription_id":null`) || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w.Code, w.Body)
	}
	w = recoveryHTTP(t, h, "POST", "/api/subscriptions", cookie, "192.0.2.3", map[string]any{"name": "old", "entries": []any{}})
	if w.Code != 400 {
		t.Fatal("legacy accepted", w.Code)
	}
	body := map[string]any{"name": "collection", "user_ids": []int64{u.ID}}
	w = recoveryHTTP(t, h, "POST", "/api/subscriptions", cookie, "192.0.2.3", body)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var created struct {
		Subscription struct {
			ID int64 `json:"id"`
		}
		Token string
	}
	if e = json.Unmarshal(w.Body.Bytes(), &created); e != nil {
		t.Fatal(e)
	}
	w = recoveryHTTP(t, h, "POST", "/api/subscriptions", cookie, "192.0.2.3", body)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	w = recoveryHTTP(t, h, "PATCH", fmt.Sprintf("/api/subscriptions/%d", created.Subscription.ID), cookie, "192.0.2.3", map[string]any{"quota_bytes": 100})
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	url := fmt.Sprintf("/api/subscriptions/%d", created.Subscription.ID)
	hashBefore, secretBefore, e := st.SubscriptionTokenMaterial(ctx, created.Subscription.ID)
	if e != nil {
		t.Fatal(e)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("PATCH", url, strings.NewReader(`{"name":"unauthorized"}`)))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, invalid := range []string{"  ", strings.Repeat("名", 65), "bad\nname"} {
		w = recoveryHTTP(t, h, "PATCH", url, cookie, "192.0.2.3", map[string]any{"name": invalid})
		if w.Code != 400 {
			t.Fatal("invalid name accepted", w.Code)
		}
	}
	w = recoveryHTTP(t, h, "PATCH", url, cookie, "192.0.2.3", map[string]any{"name": "  台湾订阅  "})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	renamed, e := st.SubscriptionByID(ctx, created.Subscription.ID)
	if e != nil || renamed.Name != "台湾订阅" || len(renamed.Members) != 1 || renamed.Members[0].UserID != u.ID {
		t.Fatal("rename or membership incorrect", e)
	}
	hashAfter, secretAfter, e := st.SubscriptionTokenMaterial(ctx, created.Subscription.ID)
	if e != nil || !bytes.Equal(hashBefore, hashAfter) || !bytes.Equal(secretBefore, secretAfter) {
		t.Fatal("rename changed credentials", e)
	}
	unchanged, e := st.SingBoxUserByID(ctx, u.ID)
	if e != nil || unchanged.Enabled || !bytes.Equal(unchanged.ConfigBlob, u.ConfigBlob) {
		t.Fatal("rename changed node", e)
	}
	w = recoveryHTTP(t, h, "PATCH", url, cookie, "192.0.2.3", map[string]any{"name": "must-not-save", "enabled": true})
	if w.Code != 403 {
		t.Fatal("rename allowed policy change", w.Code)
	}
	renamed, e = st.SubscriptionByID(ctx, created.Subscription.ID)
	if e != nil || renamed.Name != "台湾订阅" {
		t.Fatal("rejected patch changed name", e)
	}
	w = recoveryHTTP(t, h, "GET", "/sub/"+created.Token, "", "192.0.2.4", nil)
	if w.Code != 422 {
		t.Fatal(w.Code, w.Body)
	}
	w = recoveryHTTP(t, h, "DELETE", fmt.Sprintf("/api/subscriptions/%d", created.Subscription.ID), cookie, "192.0.2.3", nil)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body)
	}
	if _, e = st.SingBoxUserByID(ctx, u.ID); e != nil {
		t.Fatal("deleted real node", e)
	}
}
