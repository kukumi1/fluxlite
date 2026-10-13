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

	"github.com/kukumi1/fluxlite/internal/auth"
	"github.com/kukumi1/fluxlite/internal/store"
	"github.com/pquerna/otp/totp"
)

const apiRecoveryPassword = "correct-horse-battery"
const apiRecoveryAnswer = "Private answer not published"

func recoveryServer(t *testing.T) (*Server, *store.Store, http.Handler, string) {
	t.Helper()
	srv, st := newAuditTestServer(t)
	srv.auth = auth.New(st)
	if _, err := srv.auth.CreateInitialUser(context.Background(), "admin", apiRecoveryPassword); err != nil {
		t.Fatal(err)
	}
	token, _, err := srv.auth.Login(context.Background(), "admin", apiRecoveryPassword, "")
	if err != nil {
		t.Fatal(err)
	}
	return srv, st, srv.Handler(), token
}

func recoveryHTTP(t *testing.T, h http.Handler, method, path, token, ip string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = ip + ":12345"
	if token != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("recovery response can be cached")
	}
	return w
}

func configureRecoveryHTTP(t *testing.T, h http.Handler, token, code string) string {
	t.Helper()
	w := recoveryHTTP(t, h, "POST", "/api/account/recovery", token, "192.0.2.1", map[string]string{
		"password": apiRecoveryPassword, "code": code, "question": "My private security question?", "answer": apiRecoveryAnswer,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("configuration failed: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		RecoveryCode string `json:"recovery_code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.RecoveryCode == "" {
		t.Fatal("missing recovery code", err)
	}
	return response.RecoveryCode
}

func TestRecoveryHTTPAuthenticationAndUniformFailures(t *testing.T) {
	_, st, h, token := recoveryServer(t)
	w := recoveryHTTP(t, h, "GET", "/api/account/recovery", "", "192.0.2.1", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatal("anonymous settings read allowed", w.Code)
	}
	w = recoveryHTTP(t, h, "GET", "/api/account/recovery", token, "192.0.2.1", nil)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"enabled":false,"question":""}` {
		t.Fatal("existing user defaults changed", w.Body.String())
	}
	var uniform string
	for _, username := range []string{"admin", "unknown-user"} {
		w = recoveryHTTP(t, h, "POST", "/api/recovery/question", "", "192.0.2.2", map[string]string{"username": username, "recovery_code": "bad"})
		if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "question") {
			t.Fatal("recovery enumeration", w.Code, w.Body.String())
		}
		if uniform == "" {
			uniform = w.Body.String()
		} else if uniform != w.Body.String() {
			t.Fatal("unknown and unconfigured differ")
		}
	}
	key := configureRecoveryHTTP(t, h, token, "")
	for _, proof := range []map[string]string{
		{"username": "admin", "recovery_code": "bad", "answer": apiRecoveryAnswer, "password": "new-strong-password"},
		{"username": "unknown-user", "recovery_code": key, "answer": apiRecoveryAnswer, "password": "new-strong-password"},
		{"username": "admin", "recovery_code": key, "answer": "wrong answer", "password": "new-strong-password"},
	} {
		w = recoveryHTTP(t, h, "POST", "/api/recovery/reset", "", "192.0.2.3", proof)
		if w.Code != http.StatusUnauthorized || w.Body.String() != uniform {
			t.Fatal("proof failures differ", w.Code, w.Body.String())
		}
	}
	u, err := st.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.RecordLoginFailure(context.Background(), u.ID, 1, time.Minute); err != nil {
		t.Fatal(err)
	}
	w = recoveryHTTP(t, h, "POST", "/api/recovery/question", "", "192.0.2.3", map[string]string{"username": "admin", "recovery_code": key})
	if w.Code != http.StatusUnauthorized || w.Body.String() != uniform {
		t.Fatal("locked account disclosed", w.Code, w.Body.String())
	}
}

func TestRecoveryHTTPResetRevokesSessionsWithoutLoginOrSecretAudit(t *testing.T) {
	_, st, h, token := recoveryServer(t)
	key := configureRecoveryHTTP(t, h, token, "")
	w := recoveryHTTP(t, h, "POST", "/api/recovery/question", "", "192.0.2.2", map[string]string{"username": "admin", "recovery_code": key})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"totp_required":false`) {
		t.Fatal("question failed", w.Code, w.Body.String())
	}
	proof := map[string]string{"username": "admin", "recovery_code": key, "answer": apiRecoveryAnswer, "password": "new-strong-password"}
	w = recoveryHTTP(t, h, "POST", "/api/recovery/reset", "", "192.0.2.2", proof)
	if w.Code != http.StatusOK {
		t.Fatal("reset failed", w.Code, w.Body.String())
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == sessionCookie && (cookie.Value != "" || cookie.MaxAge >= 0) {
			t.Fatal("reset created login session")
		}
	}
	w = recoveryHTTP(t, h, "GET", "/api/account/recovery", token, "192.0.2.2", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatal("old authenticated session survived", w.Code)
	}
	w = recoveryHTTP(t, h, "POST", "/api/recovery/reset", "", "192.0.2.2", proof)
	if w.Code != http.StatusUnauthorized {
		t.Fatal("replayed recovery key", w.Code)
	}
	entries, err := st.ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{key, apiRecoveryAnswer, apiRecoveryPassword, proof["password"]} {
		if strings.Contains(string(data), secret) {
			t.Fatal("audit contains secret")
		}
	}
}

func TestRecoveryHTTPRateLimitsAllWrites(t *testing.T) {
	for _, endpoint := range []struct{ method, path string }{
		{"POST", "/api/recovery/question"}, {"POST", "/api/recovery/reset"},
		{"POST", "/api/account/recovery"}, {"DELETE", "/api/account/recovery"},
	} {
		t.Run(endpoint.method+endpoint.path, func(t *testing.T) {
			_, _, h, token := recoveryServer(t)
			for i := 0; i < 11; i++ {
				w := recoveryHTTP(t, h, endpoint.method, endpoint.path, token, "192.0.2.1", map[string]string{})
				if i < 10 && w.Code == http.StatusTooManyRequests || i == 10 && w.Code != http.StatusTooManyRequests {
					t.Fatalf("request %d response %d", i+1, w.Code)
				}
			}
		})
	}
}

func TestRecoveryHTTPTOTPIsStillRequired(t *testing.T) {
	srv, st, h, token := recoveryServer(t)
	u, err := st.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(u.TOTPSecret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.auth.EnableTOTP(context.Background(), u.ID, code); err != nil {
		t.Fatal(err)
	}
	key := configureRecoveryHTTP(t, h, token, code)
	w := recoveryHTTP(t, h, "POST", "/api/recovery/question", "", "192.0.2.2", map[string]string{"username": "admin", "recovery_code": key})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"totp_required":true`) {
		t.Fatal("TOTP requirement absent")
	}
	proof := map[string]string{"username": "admin", "recovery_code": key, "answer": apiRecoveryAnswer, "password": "new-strong-password"}
	w = recoveryHTTP(t, h, "POST", "/api/recovery/reset", "", "192.0.2.2", proof)
	if w.Code != http.StatusUnauthorized {
		t.Fatal("recovery bypassed TOTP")
	}
	proof["code"], err = totp.GenerateCode(u.TOTPSecret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	w = recoveryHTTP(t, h, "POST", "/api/recovery/reset", "", "192.0.2.2", proof)
	if w.Code != http.StatusOK {
		t.Fatal("TOTP reset failed", w.Code, w.Body.String())
	}
}

func TestRecoveryHTTPRejectsOversizeBodiesAndUnknownFields(t *testing.T) {
	_, _, h, _ := recoveryServer(t)
	for i, payload := range []string{`{"username":"admin","unexpected":"value"}`, `{"username":"` + strings.Repeat("x", 1<<20) + `"}`} {
		r := httptest.NewRequest("POST", "/api/recovery/question", strings.NewReader(payload))
		r.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", i+1)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || w.Body.String() != "{\"error\":\"请求格式错误\"}\n" {
			t.Fatal("invalid request accepted or echoed", w.Code)
		}
	}
}
