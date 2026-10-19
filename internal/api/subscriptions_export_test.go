package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/singbox"
	"github.com/kukumi1/fluxlite/internal/store"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionHTTPRealExportPipeline(t *testing.T) {
	srv, st, _, cookie := recoveryServer(t)
	ctx := context.Background()
	seal, e := cryptox.NewSealer(bytes.Repeat([]byte{3}, 32))
	if e != nil {
		t.Fatal(e)
	}
	srv.svc = service.New(st, seal, nil, nil, nil, nil, nil)
	n := &model.Node{Name: "fixture-machine", Host: "192.0.2.10", SSHPort: 22, SSHUser: "root", AuthType: model.AuthPassword, AuthSecret: []byte("test"), PortStart: 10000, PortEnd: 11000}
	if e = st.CreateNode(ctx, n); e != nil {
		t.Fatal(e)
	}
	b, e := singbox.Generate(model.SingBoxTrojan, "isolated", 10001, "/tmp/test", "")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(b)
	if e != nil {
		t.Fatal(e)
	}
	blob, e := seal.Seal(raw)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	u := &model.SingBoxUser{NodeID: n.ID, Name: "isolated", Port: 10001, Protocol: model.SingBoxTrojan, Source: model.SingBoxManaged, Enabled: true, Status: model.SingBoxStatusStopped, ServiceName: "fluxlite-singbox-test", ConfigPath: "/tmp/test", ConfigBlob: blob, PeriodStartedAt: now, PeriodEndsAt: now.Add(30 * 24 * time.Hour), PeriodDays: 30}
	if e = st.CreateSingBoxUser(ctx, u); e != nil {
		t.Fatal(e)
	}
	token := strings.Repeat("D", 43)
	hash := sha256.Sum256([]byte(token))
	v := &store.Subscription{Name: "friend", Enabled: true, Status: "active"}
	if e = st.CreateSubscription(ctx, v, hash[:]); e != nil {
		t.Fatal(e)
	}
	if e = st.AddSubscriptionMember(ctx, v.ID, store.SubscriptionMember{UserID: u.ID, NodeID: n.ID, Protocol: "trojan"}); e != nil {
		t.Fatal(e)
	}
	h := srv.Handler()
	for i, f := range []string{"clash", "base64", "shadowrocket", "loon", "qx"} {
		w := recoveryHTTP(t, h, "GET", "/sub/"+token+"?format="+f, "", fmt.Sprintf("192.0.2.%d", 50+i), nil)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s export %d", f, w.Code)
		}
		out := w.Body.Bytes()
		if f == "base64" || f == "shadowrocket" {
			out, e = base64.StdEncoding.DecodeString(string(out))
			if e != nil {
				t.Fatal(e)
			}
		}
		if !bytes.Contains(out, []byte("192.0.2.10")) || bytes.Contains(out, []byte("private_key")) || bytes.Contains(out, []byte("/tmp/")) {
			t.Fatalf("%s wrong payload", f)
		}
	}
	w := recoveryHTTP(t, h, "GET", fmt.Sprintf("/api/subscriptions/%d/preview?format=clash", v.ID), cookie, "192.0.2.90", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"included_count":1`) {
		t.Fatal("preview contract failed", w.Code)
	}
	if _, e := srv.svc.ResetSubscriptionToken(ctx, v.ID); e != nil {
		t.Fatal(e)
	}
	w = recoveryHTTP(t, h, "GET", "/sub/"+token, "", "192.0.2.91", nil)
	if w.Code != 404 {
		t.Fatal("old URL remains valid")
	}
}
