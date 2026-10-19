package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/kukumi1/fluxlite/internal/auth"
	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/service"
)

type telegramPollTransport func(*http.Request) (*http.Response, error)

func (f telegramPollTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func mockTelegramPollHTTP(t *testing.T, f telegramPollTransport) {
	t.Helper()
	old := telegramHTTPClient
	telegramHTTPClient = &http.Client{Transport: f, Timeout: time.Second}
	t.Cleanup(func() { telegramHTTPClient = old })
}
func telegramPollResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func telegramPollFixture(t *testing.T) *Server {
	t.Helper()
	s, st := newAuditTestServer(t)
	sealer, err := cryptox.NewSealer(bytes.Repeat([]byte{7}, cryptox.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	s.svc = service.New(st, sealer, nil, nil, nil, nil, slog.Default())
	s.auth = auth.New(st)
	if _, err = s.auth.CreateInitialUser(context.Background(), "operator", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	owner, err := st.UserByUsername(context.Background(), "operator")
	if err != nil {
		t.Fatal(err)
	}
	s.telegram = newTelegramState(s.svc)
	s.telegram.cfg = telegramConfig{Enabled: true, Token: "123:sample-token", UserID: owner.ID, BoundUserID: 42, PanelURL: "https://panel.example"}
	s.telegram.loaded = true
	return s
}
func TestTelegramCallDoesNotLeakToken(t *testing.T) {
	token := "123:private-token"
	mockTelegramPollHTTP(t, func(r *http.Request) (*http.Response, error) { return nil, errors.New(r.URL.String()) })
	err := telegramCall(context.Background(), token, "getMe", nil, nil)
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "https://") {
		t.Fatalf("unsafe error: %v", err)
	}
}
func TestTelegramBotRejectsGroupsAndUnboundUsers(t *testing.T) {
	s := telegramPollFixture(t)
	calls := 0
	mockTelegramPollHTTP(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return telegramPollResponse(`{"ok":true,"result":{}}`), nil
	})
	for _, m := range []telegramBotMessage{
		{From: telegramBotUser{ID: 42}, Chat: telegramBotChat{ID: -1, Type: "group"}, Text: "/start"},
		{From: telegramBotUser{ID: 43}, Chat: telegramBotChat{ID: 43, Type: "private"}, Text: "/start"},
		{From: telegramBotUser{ID: 42}, Chat: telegramBotChat{ID: 43, Type: "private"}, Text: "/start"},
	} {
		s.processTelegramUpdate(context.Background(), s.telegram.cfg, telegramUpdate{Message: &m})
	}
	if calls != 0 {
		t.Fatalf("unauthorized responses: %d", calls)
	}
}
func TestTelegramBotBindingSingleUseAndNoSecrets(t *testing.T) {
	s := telegramPollFixture(t)
	s.telegram.cfg.BoundUserID = 0
	s.telegram.bindings["nonce"] = telegramBinding{UserID: s.telegram.cfg.UserID, Expires: time.Now().Add(time.Minute)}
	calls := 0
	mockTelegramPollHTTP(t, func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), s.telegram.cfg.Token) {
			t.Fatal("token in message")
		}
		return telegramPollResponse(`{"ok":true,"result":{}}`), nil
	})
	m := telegramBotMessage{From: telegramBotUser{ID: 42, Username: "admin"}, Chat: telegramBotChat{ID: 42, Type: "private"}, Text: "/start bind_nonce"}
	s.processTelegramUpdate(context.Background(), s.telegram.cfg, telegramUpdate{Message: &m})
	if s.telegram.cfg.BoundUserID != 42 || len(s.telegram.bindings) != 0 || calls != 1 {
		t.Fatal("binding failed")
	}
	m.From.ID = 43
	m.Chat.ID = 43
	s.processTelegramUpdate(context.Background(), s.telegram.cfg, telegramUpdate{Message: &m})
	if s.telegram.cfg.BoundUserID != 42 || calls != 1 {
		t.Fatal("binding nonce reused")
	}
	logs, err := s.svc.Store().ListAudit(context.Background(), 10)
	if err != nil || len(logs) != 1 || logs[0].Actor != "telegram:42" {
		t.Fatalf("missing numeric audit: %v %v", logs, err)
	}
}
func TestTelegramBotExpiredBindingAndLockedOwner(t *testing.T) {
	s := telegramPollFixture(t)
	calls := 0
	mockTelegramPollHTTP(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return telegramPollResponse(`{"ok":true,"result":{}}`), nil
	})
	s.telegram.bindings["expired"] = telegramBinding{UserID: s.telegram.cfg.UserID, Expires: time.Now().Add(-time.Minute)}
	m := telegramBotMessage{From: telegramBotUser{ID: 43}, Chat: telegramBotChat{ID: 43, Type: "private"}, Text: "/start bind_expired"}
	s.processTelegramUpdate(context.Background(), s.telegram.cfg, telegramUpdate{Message: &m})
	if calls != 0 || s.telegram.cfg.BoundUserID != 42 {
		t.Fatal("expired binding accepted")
	}
	if _, err := s.svc.Store().DB().Exec(`UPDATE users SET locked_until=? WHERE id=?`, time.Now().Add(time.Hour), s.telegram.cfg.UserID); err != nil {
		t.Fatal(err)
	}
	m.From.ID = 42
	m.Chat.ID = 42
	m.Text = "/start"
	s.processTelegramUpdate(context.Background(), s.telegram.cfg, telegramUpdate{Message: &m})
	if calls != 0 {
		t.Fatal("locked owner read allowed")
	}
}
func TestTelegramPollLoadsPersistedConfigurationAndCheckpoints(t *testing.T) {
	s := telegramPollFixture(t)
	if err := s.telegram.save(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.telegram = newTelegramState(s.svc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	mockTelegramPollHTTP(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			return telegramPollResponse(`{"ok":true,"result":[{"update_id":80,"message":{"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"/start"}}]}`), nil
		}
		data, err := s.svc.TelegramConfig(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var saved telegramConfig
		if err = json.Unmarshal(data, &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Offset != 81 {
			t.Fatal("update handled before checkpoint")
		}
		cancel()
		return telegramPollResponse(`{"ok":true,"result":{}}`), nil
	})
	s.RunTelegram(ctx)
	if calls != 2 || !s.telegram.loaded || s.telegram.cfg.Offset != 81 {
		t.Fatalf("persisted polling failed: %d", calls)
	}
}
func TestTelegramMenuHasManagementNavigationAndWebApp(t *testing.T) {
	b, err := json.Marshal(telegramMenu("https://panel.example/", "routes"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "https://panel.example/telegram?tab=routes") || strings.Contains(string(b), "delete") || strings.Contains(string(b), "console") {
		t.Fatal(string(b))
	}
	if telegramPercent(nil, nil) != "未知" || telegramCPUPercent(nil) != "未知" {
		t.Fatal("missing metrics should not be zero")
	}
}

func TestTelegramRateLimitRetriesWithPacing(t *testing.T) {
	s := telegramPollFixture(t)
	var sends []time.Time
	mockTelegramPollHTTP(t, func(r *http.Request) (*http.Response, error) {
		sends = append(sends, time.Now())
		if len(sends) == 1 {
			resp := telegramPollResponse(`{"ok":false,"description":"secret 123:sample-token","parameters":{"retry_after":1}}`)
			resp.StatusCode = 429
			return resp, nil
		}
		return telegramPollResponse(`{"ok":true,"result":{}}`), nil
	})
	if err := s.sendTelegramMessage(context.Background(), s.telegram.cfg, 42, "test", "dashboard"); err != nil {
		t.Fatal(err)
	}
	if len(sends) != 2 || sends[1].Sub(sends[0]) < time.Second {
		t.Fatal("429 retry was not paced", sends)
	}
}
func TestTelegramRetryRevocationAndCancellation(t *testing.T) {
	for _, cancelInstead := range []bool{false, true} {
		t.Run(strconv.FormatBool(cancelInstead), func(t *testing.T) {
			s := telegramPollFixture(t)
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mockTelegramPollHTTP(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if cancelInstead {
					cancel()
				} else {
					s.telegram.mu.Lock()
					s.telegram.cfg.BoundUserID = 0
					s.telegram.mu.Unlock()
				}
				resp := telegramPollResponse(`{"ok":false,"parameters":{"retry_after":1}}`)
				resp.StatusCode = 429
				return resp, nil
			})
			if err := s.sendTelegramMessage(ctx, s.telegram.cfg, 42, "test", "dashboard"); err == nil {
				t.Fatal("revoked or cancelled send succeeded")
			}
			if calls != 1 {
				t.Fatal("retried after revocation/cancellation", calls)
			}
		})
	}
}
func TestTelegramRateLimitIsBoundedAndSanitized(t *testing.T) {
	for _, body := range []string{`{"description":"123:sample-token","parameters":{"retry_after":999999999}}`, `{"description":"123:sample-token","parameters":{"retry_after":-1}}`, `invalid 123:sample-token`} {
		mockTelegramPollHTTP(t, func(r *http.Request) (*http.Response, error) {
			resp := telegramPollResponse(body)
			resp.StatusCode = 429
			return resp, nil
		})
		err := telegramCall(context.Background(), "123:sample-token", "sendMessage", nil, nil)
		var limited *telegramRateLimitError
		if !errors.As(err, &limited) || limited.RetryAfter != 0 || strings.Contains(err.Error(), "sample-token") {
			t.Fatalf("unsafe rate-limit error: %v", err)
		}
	}
}
func TestTelegramLongNamesAndUTF16MessageBound(t *testing.T) {
	name := strings.Repeat("长名称😀\n\t", 1500)
	shown := telegramDisplayName(name)
	if len([]rune(shown)) > 80 || strings.ContainsAny(shown, "\n\t") {
		t.Fatal("unbounded or multiline display name")
	}
	text := telegramMessageText(strings.Repeat(name, 15))
	if len(utf16.Encode([]rune(text))) > 4096 || !utf8.ValidString(text) || !strings.Contains(text, "更多内容") {
		t.Fatal("message exceeds Telegram limit or invalid UTF8")
	}
	if telegramMessageText("正常😀") != "正常😀" {
		t.Fatal("short message changed")
	}
}
