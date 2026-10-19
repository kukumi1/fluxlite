package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/service"
)

func tgTestNode(t *testing.T, s *Server) *model.Node {
	t.Helper()
	n := &model.Node{Name: "test", Host: "192.0.2.1", SSHPort: 22, SSHUser: "root", AuthType: model.AuthPassword, AuthSecret: []byte("encrypted-test"), PortStart: 10000, PortEnd: 65535}
	if e := s.svc.Store().CreateNode(context.Background(), n); e != nil {
		t.Fatal(e)
	}
	return n
}
func tgTestConfirm(t *testing.T, s *Server, n *model.Node) string {
	t.Helper()
	_, b, _, e := s.telegramDispatch(context.Background(), telegramBotUser{ID: 42}, fmt.Sprintf("act:nodes:delete:%d", n.ID), true)
	if e != nil {
		t.Fatal(e)
	}
	return b[0][0]["callback_data"].(string)
}
func TestTelegramConfirmationSingleUse(t *testing.T) {
	s := telegramPollFixture(t)
	n := tgTestNode(t, s)
	command := tgTestConfirm(t, s, n)
	from := telegramBotUser{ID: 42, Username: "admin"}
	_, _, _, e := s.telegramDispatch(context.Background(), from, command, true)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.svc.Store().NodeByID(context.Background(), n.ID); e == nil {
		t.Fatal("node not deleted")
	}
	if _, _, _, e = s.telegramDispatch(context.Background(), from, command, true); e == nil {
		t.Fatal("replayed confirmation accepted")
	}
	var actor string
	if e = s.svc.Store().DB().QueryRow(`SELECT actor FROM audit_logs WHERE action='telegram.nodes:delete'`).Scan(&actor); e != nil || actor != "telegram:42:operator" {
		t.Fatalf("audit actor %q: %v", actor, e)
	}
}
func TestTelegramConfirmationRejectsInvalidScope(t *testing.T) {
	for _, kind := range []string{"expired", "actor", "revoked", "generation", "password", "changed", "owner"} {
		t.Run(kind, func(t *testing.T) {
			s := telegramPollFixture(t)
			n := tgTestNode(t, s)
			command := tgTestConfirm(t, s, n)
			nonce := strings.TrimPrefix(command, "confirm:")
			c := s.telegram.confirmations[nonce]
			from := telegramBotUser{ID: 42}
			switch kind {
			case "expired":
				c.Scope.Expires = time.Now().Add(-time.Second)
				s.telegram.confirmations[nonce] = c
			case "actor":
				from.ID = 43
			case "revoked":
				s.telegram.revoke()
			case "generation":
				s.telegram.generation++
			case "password":
				if _, e := s.svc.Store().DB().Exec(`UPDATE users SET password_hash='changed' WHERE id=?`, s.telegram.cfg.UserID); e != nil {
					t.Fatal(e)
				}
			case "changed":
				if _, e := s.svc.Store().DB().Exec(`UPDATE nodes SET host='192.0.2.2' WHERE id=?`, n.ID); e != nil {
					t.Fatal(e)
				}
			case "owner":
				s.telegram.cfg.UserID++
			}
			if _, _, _, e := s.telegramDispatch(context.Background(), from, command, true); e == nil {
				t.Fatal("invalid confirmation accepted")
			}
			if _, e := s.svc.Store().NodeByID(context.Background(), n.ID); e != nil {
				t.Fatal("invalid confirmation deleted node")
			}
		})
	}
}
func TestTelegramWizardValidationCancelAndStaleButtons(t *testing.T) {
	s := telegramPollFixture(t)
	from := telegramBotUser{ID: 42}
	ctx := context.Background()
	if _, _, _, e := s.telegramDispatch(ctx, from, "add:routes", true); e != nil {
		t.Fatal(e)
	}
	w := s.telegram.wizards[42]
	nonce := w.Nonce
	if _, _, _, e := s.telegramDispatch(ctx, from, "example route", false); e != nil {
		t.Fatal(e)
	}
	if w.Stage != "hops" {
		t.Fatal(w.Stage)
	}
	if _, _, _, e := s.telegramDispatch(ctx, from, "wiz:"+nonce+":protocol:tcp", true); e == nil {
		t.Fatal("wrong stage accepted")
	}
	if _, _, _, e := s.telegramDispatch(ctx, from, "wiz:"+nonce+":hops:done", true); e != nil {
		t.Fatal(e)
	}
	if w.Stage != "hops" {
		t.Fatal("empty hops accepted")
	}
	n := tgTestNode(t, s)
	for _, cmd := range []string{fmt.Sprintf("wiz:%s:hops:%d", nonce, n.ID), "wiz:" + nonce + ":hops:done"} {
		if _, _, _, e := s.telegramDispatch(ctx, from, cmd, true); e != nil {
			t.Fatal(e)
		}
	}
	if _, _, _, e := s.telegramDispatch(ctx, from, "not-a-host-port", false); e != nil {
		t.Fatal(e)
	}
	if w.Stage != "target" {
		t.Fatal("invalid target advanced")
	}
	for _, step := range []struct {
		value    string
		callback bool
	}{{"example.com:443", false}, {"wiz:" + nonce + ":protocol:tcp", true}, {"auto", false}, {"0", false}} {
		if _, _, _, e := s.telegramDispatch(ctx, from, step.value, step.callback); e != nil {
			t.Fatal(e)
		}
	}
	if len(s.telegram.confirmations) != 1 {
		t.Fatal("missing confirmation")
	}
	for _, c := range s.telegram.confirmations {
		if c.Wizard.Route.Enabled || c.Wizard.Route.QuotaBytes != nil || len(c.Nodes) != 1 {
			t.Fatal("unsafe route defaults")
		}
	}
	if _, _, _, e := s.telegramDispatch(ctx, from, "/cancel", false); e != nil {
		t.Fatal(e)
	}
	if len(s.telegram.confirmations) != 0 || len(s.telegram.wizards) != 0 {
		t.Fatal("cancel did not clear state")
	}
}
func TestTelegramMachineDraftSafeAndScoped(t *testing.T) {
	s := telegramPollFixture(t)
	ctx := context.Background()
	from := telegramBotUser{ID: 42}
	_, _, _, e := s.telegramDispatch(ctx, from, "add:nodes", true)
	if e != nil {
		t.Fatal(e)
	}
	for _, input := range []string{"machine", "192.0.2.2", "-", "-", "-", "-"} {
		if _, _, _, e = s.telegramDispatch(ctx, from, input, false); e != nil {
			t.Fatal(e)
		}
	}
	var nonce string
	for key := range s.telegram.machineDrafts {
		nonce = key
	}
	if nonce == "" {
		t.Fatal("missing machine draft")
	}
	draft := s.telegram.machineDrafts[nonce]
	if draft.Input.Secret != "" || draft.Input.SSHPort != 22 || draft.Input.SSHUser != "root" {
		t.Fatal("unsafe draft")
	}
	s.telegram.sessions["session"] = telegramSession{UserID: draft.Scope.Owner, Actor: 42, Credentials: draft.Scope.Credentials, Expires: time.Now().Add(time.Minute)}
	read := func(actor int64) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/telegram/machine-draft?draft="+nonce, nil)
		r = r.WithContext(context.WithValue(r.Context(), telegramActorKey, actor))
		w := httptest.NewRecorder()
		s.handleTelegramMachineDraft(w, r)
		return w
	}
	for i := 0; i < 2; i++ {
		w := read(42)
		if w.Code != 200 || strings.Contains(w.Body.String(), `"secret"`) {
			t.Fatalf("safe repeated read: %d %s", w.Code, w.Body.String())
		}
	}
	if read(43).Code != 404 {
		t.Fatal("wrong actor read")
	}
	r := httptest.NewRequest(http.MethodGet, "/api/telegram/machine-draft?draft="+nonce, nil)
	r.Header.Set("Authorization", "Bearer session")
	w := httptest.NewRecorder()
	s.authenticateTelegram(w, r, http.HandlerFunc(s.handleTelegramMachineDraft))
	if w.Code != 200 {
		t.Fatalf("bearer read: %d", w.Code)
	}
	if !telegramRouteAllowed("GET", "/api/telegram/machine-draft") || telegramRouteAllowed("POST", "/api/telegram/machine-draft") || telegramRouteAllowed("GET", "/api/telegram/machine-draft/extra") {
		t.Fatal("draft allowlist")
	}
	s.telegram.revoke()
	if read(42).Code != 404 {
		t.Fatal("revoked draft readable")
	}
}
func TestTelegramDraftExpiredAndFingerprint(t *testing.T) {
	for _, expired := range []bool{false, true} {
		s := telegramPollFixture(t)
		scope, e := s.newTelegramScope(context.Background(), 42)
		if e != nil {
			t.Fatal(e)
		}
		if expired {
			scope.Expires = time.Now().Add(-time.Second)
		} else {
			scope.Credentials = [32]byte{1}
		}
		s.telegram.machineDrafts["nonce"] = telegramMachineDraft{Scope: scope, Input: service.NodeInput{Name: "test"}}
		r := httptest.NewRequest("GET", "/api/telegram/machine-draft?draft=nonce", nil)
		r = r.WithContext(context.WithValue(r.Context(), telegramActorKey, int64(42)))
		w := httptest.NewRecorder()
		s.handleTelegramMachineDraft(w, r)
		if w.Code != 404 {
			t.Fatal("invalid draft read")
		}
	}
}
func TestTelegramNumericInputValidation(t *testing.T) {
	for _, value := range []string{"NaN", "Inf", "-1", "999999999999999999999999"} {
		if _, e := tgQuota(value); e == nil {
			t.Fatalf("accepted quota %q", value)
		}
	}
	for _, value := range []string{"0", "65536", "-1", "x"} {
		if _, e := tgPort(value, false); e == nil {
			t.Fatalf("accepted port %q", value)
		}
	}
	if n, e := tgQuota("0.5"); e != nil || n != 1<<29 {
		t.Fatal("fractional quota")
	}
}

func TestTelegramProxyProtocolWizardConfirmation(t *testing.T) {
	for _, protocol := range []string{"ss2022", "anytls", "vless-reality", "hysteria2", "tuic", "trojan", "vmess", "vless-tls"} {
		t.Run(protocol, func(t *testing.T) {
			s := telegramPollFixture(t)
			n := tgTestNode(t, s)
			from := telegramBotUser{ID: 42}
			ctx := context.Background()
			if _, _, _, e := s.telegramDispatch(ctx, from, "add:singbox", true); e != nil {
				t.Fatal(e)
			}
			nonce := s.telegram.wizards[42].Nonce
			for _, step := range []struct {
				v string
				c bool
			}{{fmt.Sprintf("wiz:%s:machine:%d", nonce, n.ID), true}, {"proxy", false}, {"wiz:" + nonce + ":protocol:" + protocol, true}, {"auto", false}, {"0.5", false}, {"30", false}} {
				if _, _, _, e := s.telegramDispatch(ctx, from, step.v, step.c); e != nil {
					t.Fatal(e)
				}
			}
			if len(s.telegram.confirmations) != 1 {
				t.Fatal("missing create confirmation")
			}
			for _, c := range s.telegram.confirmations {
				if c.Action != "singbox:create" || string(c.Wizard.Proxy.Protocol) != protocol || c.Wizard.Proxy.NodeID != n.ID || c.Wizard.Proxy.QuotaBytes != 1<<29 || c.Wizard.Proxy.AccessDays != 30 || len(c.Nodes) != 1 {
					t.Fatalf("bad create input: %+v", c.Wizard.Proxy)
				}
			}
			if _, _, _, e := s.telegramDispatch(ctx, from, "wiz:"+nonce+":protocol:"+protocol, true); e == nil {
				t.Fatal("completed wizard callback accepted")
			}
		})
	}
}
func TestTelegramWizardOrderedMultipleHops(t *testing.T) {
	s := telegramPollFixture(t)
	first := tgTestNode(t, s)
	second := &model.Node{Name: "second", Host: "192.0.2.2", SSHPort: 22, SSHUser: "root", AuthType: model.AuthPassword, AuthSecret: []byte("test"), PortStart: 10000, PortEnd: 65535}
	ctx := context.Background()
	if e := s.svc.Store().CreateNode(ctx, second); e != nil {
		t.Fatal(e)
	}
	from := telegramBotUser{ID: 42}
	s.telegramDispatch(ctx, from, "add:routes", true)
	s.telegramDispatch(ctx, from, "ordered", false)
	w := s.telegram.wizards[42]
	for _, id := range []int64{second.ID, first.ID, second.ID} {
		if _, _, _, e := s.telegramDispatch(ctx, from, fmt.Sprintf("wiz:%s:hops:%d", w.Nonce, id), true); e != nil {
			t.Fatal(e)
		}
	}
	if len(w.Route.NodeIDs) != 2 || w.Route.NodeIDs[0] != second.ID || w.Route.NodeIDs[1] != first.ID {
		t.Fatal("hop order or duplicate handling")
	}
}
func TestTelegramConfirmationConcurrentReplay(t *testing.T) {
	s := telegramPollFixture(t)
	n := tgTestNode(t, s)
	cmd := tgTestConfirm(t, s, n)
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, _, _, e := s.telegramDispatch(context.Background(), telegramBotUser{ID: 42}, cmd, true)
			results <- e
		}()
	}
	success := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("got %d executions", success)
	}
}

func TestTelegramMachineDeleteBindsAffectedProxySet(t *testing.T) {
	s := telegramPollFixture(t)
	n := tgTestNode(t, s)
	ctx := context.Background()
	addProxy := func(name string, port int) {
		t.Helper()
		u := &model.SingBoxUser{NodeID: n.ID, Name: name, Protocol: model.SingBoxSS2022, Port: port, Source: model.SingBoxManaged, ConfigBlob: []byte("encrypted"), PeriodStartedAt: time.Now(), PeriodEndsAt: time.Now().Add(30 * 24 * time.Hour), PeriodDays: 30}
		if e := s.svc.Store().CreateSingBoxUser(ctx, u); e != nil {
			t.Fatal(e)
		}
	}
	addProxy("one", 10001)
	addProxy("two", 10002)
	text, b, _, e := s.telegramDispatch(ctx, telegramBotUser{ID: 42}, fmt.Sprintf("act:nodes:delete:%d", n.ID), true)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(text, "2 个托管代理节点") {
		t.Fatal(text)
	}
	command := b[0][0]["callback_data"].(string)
	addProxy("three", 10003)
	if _, _, _, e = s.telegramDispatch(ctx, telegramBotUser{ID: 42}, command, true); e == nil {
		t.Fatal("changed affected set accepted")
	}
	if _, e = s.svc.Store().NodeByID(ctx, n.ID); e != nil {
		t.Fatal("machine removed despite changed dependencies")
	}
}

func TestTelegramExternalProxyDeleteIsRecordOnly(t *testing.T) {
	s := telegramPollFixture(t)
	n := tgTestNode(t, s)
	ctx := context.Background()
	u := &model.SingBoxUser{NodeID: n.ID, Name: "external", Protocol: model.SingBoxSS2022, Port: 12000, Source: model.SingBoxExternal, ConfigBlob: []byte("encrypted"), PeriodStartedAt: time.Now(), PeriodEndsAt: time.Now().Add(30 * 24 * time.Hour), PeriodDays: 30}
	if e := s.svc.Store().CreateSingBoxUser(ctx, u); e != nil {
		t.Fatal(e)
	}
	text, b, _, e := s.telegramDispatch(ctx, telegramBotUser{ID: 42}, fmt.Sprintf("act:singbox:delete:%d", u.ID), true)
	if e != nil || !strings.Contains(text, "仅删除扫描记录") {
		t.Fatalf("confirmation: %s %v", text, e)
	}
	if _, _, _, e = s.telegramDispatch(ctx, telegramBotUser{ID: 42}, b[0][0]["callback_data"].(string), true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.svc.Store().SingBoxUserByID(ctx, u.ID); e == nil {
		t.Fatal("record not deleted")
	}
}
func TestTelegramRouteDeleteReportsOfflineLeftovers(t *testing.T) {
	s := telegramPollFixture(t)
	n := tgTestNode(t, s)
	ctx := context.Background()
	if _, e := s.svc.Store().DB().Exec(`UPDATE nodes SET status='offline' WHERE id=?`, n.ID); e != nil {
		t.Fatal(e)
	}
	r := &model.Route{Name: "route", Slug: "route", EntryPort: 12000, Target: "example.com:443", Protocol: model.Protocol("tcp"), Hops: []model.RouteHop{{NodeID: n.ID, HopOrder: 0, RelayPort: 12000}}}
	if e := s.svc.Store().CreateRoute(ctx, r); e != nil {
		t.Fatal(e)
	}
	_, b, _, e := s.telegramDispatch(ctx, telegramBotUser{ID: 42}, fmt.Sprintf("act:routes:delete:%d", r.ID), true)
	if e != nil {
		t.Fatal(e)
	}
	text, _, _, e := s.telegramDispatch(ctx, telegramBotUser{ID: 42}, b[0][0]["callback_data"].(string), true)
	if e != nil || !strings.Contains(text, "1 个远端服务可能残留") {
		t.Fatalf("leftovers: %s %v", text, e)
	}
	if _, e = s.svc.Store().RouteByID(ctx, r.ID); e == nil {
		t.Fatal("route not deleted")
	}
}
func TestTelegramListKeepsMetricsAndTrafficNavigation(t *testing.T) {
	s := telegramPollFixture(t)
	tgTestNode(t, s)
	text, _, e := s.telegramList(context.Background(), "nodes", 0)
	if e != nil || !strings.Contains(text, "CPU 未知") || !strings.Contains(text, "内存 未知") || !strings.Contains(text, "磁盘 未知") {
		t.Fatalf("metrics: %s %v", text, e)
	}
	_, buttons, e := s.telegramList(context.Background(), "routes", 0)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, row := range buttons {
		for _, b := range row {
			if b["callback_data"] == "traffic:0" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("missing traffic navigation")
	}
}

func TestTelegramProxyIdentityRejectsAdoptionOrConfigChanges(t *testing.T) {
	for _, change := range []string{"source='managed'", "service_name='changed'", "config_blob=x'010203'"} {
		t.Run(change, func(t *testing.T) {
			s := telegramPollFixture(t)
			n := tgTestNode(t, s)
			ctx := context.Background()
			u := &model.SingBoxUser{NodeID: n.ID, Name: "external", Protocol: model.SingBoxSS2022, Port: 12000, Source: model.SingBoxExternal, ConfigBlob: []byte("encrypted"), PeriodStartedAt: time.Now(), PeriodEndsAt: time.Now().Add(30 * 24 * time.Hour), PeriodDays: 30}
			if e := s.svc.Store().CreateSingBoxUser(ctx, u); e != nil {
				t.Fatal(e)
			}
			_, b, _, e := s.telegramDispatch(ctx, telegramBotUser{ID: 42}, fmt.Sprintf("act:singbox:delete:%d", u.ID), true)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.svc.Store().DB().Exec("UPDATE singbox_users SET "+change+" WHERE id=?", u.ID); e != nil {
				t.Fatal(e)
			}
			if _, _, _, e = s.telegramDispatch(ctx, telegramBotUser{ID: 42}, b[0][0]["callback_data"].(string), true); e == nil {
				t.Fatal("changed proxy accepted")
			}
			if _, e = s.svc.Store().SingBoxUserByID(ctx, u.ID); e != nil {
				t.Fatal("changed proxy deleted")
			}
		})
	}
}
