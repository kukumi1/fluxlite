package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/service"
)

func signedTelegramData(token string, id int64, stamp int64) string {
	v := url.Values{"auth_date": {strconv.FormatInt(stamp, 10)}, "user": {`{"id":` + strconv.FormatInt(id, 10) + `}`}, "query_id": {"test"}}
	keys := []string{}
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := []string{}
	for _, k := range keys {
		lines = append(lines, k+"="+v.Get(k))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	v.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return v.Encode()
}
func TestTelegramValidation(t *testing.T) {
	now := time.Now()
	token := "123:test-token"
	raw := signedTelegramData(token, 99, now.Unix())
	id, _, err := validateTelegramInit(raw, token, now)
	if err != nil || id != 99 {
		t.Fatal("valid signature rejected", err)
	}
	for _, v := range []string{raw + "&user=duplicate", signedTelegramData("other", 99, now.Unix()), signedTelegramData(token, 99, now.Add(-6*time.Minute).Unix()), signedTelegramData(token, 99, now.Add(time.Minute).Unix()), signedTelegramData(token, -1, now.Unix())} {
		if _, _, err := validateTelegramInit(v, token, now); err == nil {
			t.Fatal("accepted invalid init data")
		}
	}
}
func TestTelegramAllowlist(t *testing.T) {
	for _, p := range []string{"/api/account/telegram", "/api/me", "/api/audit", "/api/console/unlock", "/api/nodes/1/console", "/api/nodes/1/realm", "/api/nodes/1/reenroll", "/api/enroll/ticket", "/api/password", "/api/nodes/-1", "/api/singbox/users/1/nope"} {
		for _, m := range []string{"GET", "POST", "PUT", "DELETE"} {
			if telegramRouteAllowed(m, p) {
				t.Fatalf("allowed %s %s", m, p)
			}
		}
	}
	for _, tc := range [][2]string{{"GET", "/api/metrics"}, {"GET", "/api/nodes"}, {"POST", "/api/nodes"}, {"DELETE", "/api/routes/1"}, {"POST", "/api/routes/1/apply"}, {"GET", "/api/singbox/users/1/export"}} {
		if !telegramRouteAllowed(tc[0], tc[1]) {
			t.Fatal("required route denied", tc)
		}
	}
}
func TestTelegramPanelURL(t *testing.T) {
	for _, v := range []string{"http://example.com", "https://user:pass@example.com", "https://localhost", "https://127.0.0.1", "https://10.1.1.1", "https://example.com?x=1", "https://example.com#x", "https://example.com/path"} {
		if validTelegramPanelURL(v) {
			t.Fatal("accepted", v)
		}
	}
	if !validTelegramPanelURL("https://panel.example.com") {
		t.Fatal("public HTTPS rejected")
	}
}
func TestTelegramFramingIsolation(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for _, path := range []string{"/", "/telegram", "/telegram/", "/api/nodes"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		c := w.Header().Get("Content-Security-Policy")
		if strings.HasPrefix(path, "/telegram") {
			if w.Header().Get("X-Frame-Options") != "" || !strings.Contains(c, "https://web.telegram.org") {
				t.Fatal("mini app cannot frame")
			}
		} else if w.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(c, "frame-ancestors 'none'") {
			t.Fatal("panel framing relaxed")
		}
	}
}
func TestTelegramSessionsReplayRevocationAndPersistence(t *testing.T) {
	srv, st, _, cookie := recoveryServer(t)
	seal, err := cryptox.NewSealer(bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	srv.svc = service.New(st, seal, nil, nil, nil, nil, nil)
	srv.telegram = newTelegramState(srv.svc)
	tg := srv.telegram
	tg.cfg = telegramConfig{Enabled: true, Token: "123:test-token", UserID: 1, BoundUserID: 99, PanelURL: "https://example.com"}
	tg.loaded = true
	if err := tg.save(context.Background()); err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	if err := st.DB().QueryRow(`SELECT config FROM telegram_integration`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(tg.cfg.Token)) {
		t.Fatal("token persisted plaintext")
	}
	fresh := newTelegramState(srv.svc)
	if err := fresh.load(context.Background()); err != nil || fresh.cfg.Token != tg.cfg.Token {
		t.Fatal("persistence failed", err)
	}
	h := srv.Handler()
	request := func(raw string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"init_data": raw})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/api/telegram/auth", bytes.NewReader(b)))
		return w
	}
	if w := request(signedTelegramData(tg.cfg.Token, 100, time.Now().Unix())); w.Code != 401 {
		t.Fatal("wrong actor accepted")
	}
	raw := signedTelegramData(tg.cfg.Token, 99, time.Now().Unix())
	w := request(raw)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Token string `json:"token"`
	}
	json.Unmarshal(w.Body.Bytes(), &result)
	if w = request(raw); w.Code != 401 {
		t.Fatal("replay accepted")
	}
	check := func(path, bearer string, want int) {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+bearer)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s got %d want %d %s", path, w.Code, want, w.Body.String())
		}
	}
	check("/api/nodes", result.Token, 200)
	check("/api/account/telegram", result.Token, 403)
	check("/api/nodes", "invalid", 401)
	tg.revoke()
	check("/api/nodes", result.Token, 401)
	// Normal panel authentication remains valid after Telegram revocation.
	r := httptest.NewRequest("GET", "/api/nodes", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("normal cookie affected")
	}
}

func TestTelegramConfigurationRequiresCredentialsAndRevokes(t *testing.T) {
	srv, st, _, cookie := recoveryServer(t)
	seal, err := cryptox.NewSealer(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	srv.svc = service.New(st, seal, nil, nil, nil, nil, nil)
	srv.telegram = newTelegramState(srv.svc)
	tg := srv.telegram
	tg.loaded = true
	tg.cfg = telegramConfig{Enabled: true, Token: "123:never-expose-this-token", BotUsername: "example_bot", PanelURL: "https://example.com", UserID: 1, BoundUserID: 99}
	if err := tg.save(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	call := func(method, path string, body any, origin string) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), tg.cfg.Token) {
			t.Fatal("Bot token exposed")
		}
		return w
	}
	if w := call("GET", "/api/account/telegram", nil, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	input := map[string]any{"password": "wrong", "enabled": false, "panel_url": "https://example.com"}
	if w := call("PUT", "/api/account/telegram", input, ""); w.Code != 401 {
		t.Fatal("wrong password accepted", w.Code)
	}
	input["password"] = apiRecoveryPassword
	if w := call("PUT", "/api/account/telegram", input, "https://evil.example"); w.Code != 403 {
		t.Fatal("cross-origin config accepted")
	}
	tg.sessions["session"] = telegramSession{UserID: 1, Actor: 99, Expires: time.Now().Add(time.Hour)}
	if w := call("PUT", "/api/account/telegram", input, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if tg.cfg.Enabled || len(tg.sessions) > 0 {
		t.Fatal("disable failed to revoke")
	}
	tg.cfg.Enabled = true
	if w := call("POST", "/api/account/telegram/bind", map[string]string{"password": apiRecoveryPassword}, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(tg.bindings) != 1 {
		t.Fatal("binding nonce not issued")
	}
	if w := call("DELETE", "/api/account/telegram/bind", map[string]string{"password": apiRecoveryPassword}, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if tg.cfg.BoundUserID != 0 || len(tg.bindings) != 0 {
		t.Fatal("unbind did not revoke identity and nonce")
	}
	fresh := newTelegramState(srv.svc)
	if err := fresh.load(context.Background()); err != nil || fresh.cfg.BoundUserID != 0 {
		t.Fatal("unbind not persisted")
	}
}
