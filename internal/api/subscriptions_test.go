package api

import (
	"context"
	"crypto/sha256"
	"github.com/kukumi1/fluxlite/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionHTTPRevocationAndPrivacy(t *testing.T) {
	_, st, h, session := recoveryServer(t)
	ctx := context.Background()
	token := strings.Repeat("A", 43)
	hash := sha256.Sum256([]byte(token))
	v := &store.Subscription{Name: "private group", Enabled: false, Status: "stopping", QuotaBytes: 100}
	if e := st.CreateSubscription(ctx, v, hash[:]); e != nil {
		t.Fatal(e)
	}
	var first string
	for _, secret := range []string{"invalid", token, strings.Repeat("B", 43)} {
		w := recoveryHTTP(t, h, "GET", "/sub/"+secret, "", "192.0.2.2", nil)
		if w.Code != 404 {
			t.Fatalf("unavailable token response %d %s", w.Code, w.Body)
		}
		if first == "" {
			first = w.Body.String()
		} else if w.Body.String() != first {
			t.Fatal("uniform 404 violated")
		}
	}
	w := recoveryHTTP(t, h, "GET", "/api/subscriptions", session, "192.0.2.3", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), token) || strings.Contains(w.Body.String(), "token_hash") {
		t.Fatalf("list leaked or failed %s", w.Body)
	}
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		if telegramRouteAllowed(method, "/api/subscriptions") {
			t.Fatal("TG accidentally allowed subscriptions")
		}
	}
	for i := 0; i < 11; i++ {
		w = recoveryHTTP(t, h, "GET", "/sub/invalid", "", "192.0.2.4", nil)
	}
	if w.Code != 429 {
		t.Fatalf("public downloads not rate limited: %d", w.Code)
	}
	req := httptest.NewRequest("GET", "/api/subscriptions", nil)
	req.RemoteAddr = "192.0.2.5:1234"
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	if out.Code != http.StatusUnauthorized {
		t.Fatal("anonymous list allowed")
	}
	expiry := time.Now().Add(-time.Hour)
	v.Enabled = true
	v.Status = "active"
	v.ExpiresAt = &expiry
	if e := st.UpdateSubscription(ctx, v); e != nil {
		t.Fatal(e)
	}
	w = recoveryHTTP(t, h, "GET", "/sub/"+token, "", "192.0.2.6", nil)
	if w.Code != 404 {
		t.Fatal("expired token allowed")
	}
}
