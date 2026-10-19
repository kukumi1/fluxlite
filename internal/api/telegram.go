package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/store"
)

type telegramConfig struct {
	Enabled                                     bool
	Token, BotUsername, PanelURL, BoundUsername string
	UserID, BoundUserID, Offset                 int64
}
type telegramBinding struct {
	UserID  int64
	Expires time.Time
}
type telegramSession struct {
	Credentials   [32]byte
	UserID, Actor int64
	Expires       time.Time
}
type telegramState struct {
	mu            sync.Mutex
	generation    uint64
	wizards       map[int64]*telegramWizard
	confirmations map[string]telegramConfirmation
	machineDrafts map[string]telegramMachineDraft
	cfg           telegramConfig
	bindings      map[string]telegramBinding
	sessions      map[string]telegramSession
	used          map[string]time.Time
	svc           *service.Service
	status        string
	loaded        bool
}

func newTelegramState(svc *service.Service) *telegramState {
	return &telegramState{wizards: map[int64]*telegramWizard{}, confirmations: map[string]telegramConfirmation{}, machineDrafts: map[string]telegramMachineDraft{}, svc: svc, bindings: map[string]telegramBinding{}, sessions: map[string]telegramSession{}, used: map[string]time.Time{}, status: "未启用"}
}
func (t *telegramState) load(ctx context.Context) error {
	if t.loaded {
		return nil
	}
	if t.svc == nil {
		return errors.New("service unavailable")
	}
	b, err := t.svc.TelegramConfig(ctx)
	if err != nil {
		return err
	}
	if len(b) > 0 {
		if err = json.Unmarshal(b, &t.cfg); err != nil {
			return err
		}
	}
	t.loaded = true
	return nil
}
func (t *telegramState) save(ctx context.Context) error {
	b, err := json.Marshal(t.cfg)
	if err != nil {
		return err
	}
	return t.svc.SaveTelegramConfig(ctx, b)
}
func (t *telegramState) revoke() {
	t.generation++
	t.wizards = map[int64]*telegramWizard{}
	t.confirmations = map[string]telegramConfirmation{}
	t.machineDrafts = map[string]telegramMachineDraft{}
	t.sessions = map[string]telegramSession{}
	t.bindings = map[string]telegramBinding{}
}

func validTelegramPanelURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || !strings.Contains(h, ".") {
		return false
	}
	if ip := net.ParseIP(h); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()) {
		return false
	}
	return true
}

var telegramTokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]{20,}$`)

func (s *Server) telegramCredentials(w http.ResponseWriter, r *http.Request, password, code string) bool {
	origin := r.Header.Get("Origin")
	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) || (u.Scheme != "http" && u.Scheme != "https") {
			writeError(w, 403, "来源不允许")
			return false
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeError(w, 403, "来源不允许")
		return false
	}
	if err := s.auth.VerifyPasswordAndCode(r.Context(), userFrom(r.Context()).ID, password, code); err != nil {
		writeError(w, 401, "密码或验证码错误")
		return false
	}
	return true
}
func (s *Server) handleTelegramStatus(w http.ResponseWriter, r *http.Request) {
	t := s.telegram
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.load(r.Context()); err != nil {
		writeError(w, 500, "读取 Telegram 配置失败")
		return
	}
	c := t.cfg
	bound := ""
	if c.BoundUserID > 0 {
		bound = strconv.FormatInt(c.BoundUserID, 10)
	}
	writeJSON(w, 200, map[string]any{"enabled": c.Enabled, "configured": c.Token != "", "bot_username": c.BotUsername, "panel_url": c.PanelURL, "bound_user_id": bound, "bound_username": c.BoundUsername, "status": t.status})
}
func (s *Server) handleTelegramConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled  bool   `json:"enabled"`
		Token    string `json:"token"`
		PanelURL string `json:"panel_url"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if decodeJSON(r, &req) != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if !s.telegramCredentials(w, r, req.Password, req.Code) {
		return
	}
	t := s.telegram
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.load(r.Context()) != nil {
		writeError(w, 500, "读取配置失败")
		return
	}
	c := t.cfg
	c.Enabled = req.Enabled
	if req.PanelURL != "" {
		if !validTelegramPanelURL(req.PanelURL) {
			writeError(w, 400, "面板地址必须是公网 HTTPS 根地址，不含查询参数或片段")
			return
		}
		c.PanelURL = strings.TrimRight(req.PanelURL, "/")
	}
	if req.Token != "" {
		if !telegramTokenPattern.MatchString(req.Token) {
			writeError(w, 400, "Bot Token 格式错误")
			return
		}
		var me struct {
			ID       int64  `json:"id"`
			IsBot    bool   `json:"is_bot"`
			Username string `json:"username"`
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if telegramCall(ctx, req.Token, "getMe", map[string]any{}, &me) != nil || !me.IsBot || me.Username == "" {
			writeError(w, 400, "无法验证 Bot Token，请检查 Token 和网络")
			return
		}
		if c.Token != req.Token {
			c.BoundUserID = 0
			c.BoundUsername = ""
			c.Offset = 0
		}
		c.Token = req.Token
		c.BotUsername = me.Username
	}
	if c.Enabled && (c.Token == "" || c.PanelURL == "") {
		writeError(w, 400, "启用前请配置 Bot Token 和面板地址")
		return
	}
	if c.UserID != userFrom(r.Context()).ID {
		c.BoundUserID = 0
		c.BoundUsername = ""
	}
	c.UserID = userFrom(r.Context()).ID
	old := t.cfg
	t.cfg = c
	if t.save(r.Context()) != nil {
		t.cfg = old
		writeError(w, 500, "保存配置失败")
		return
	}
	t.revoke()
	t.status = "配置已保存"
	s.audit(r, "telegram.configure", "telegram", "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}
func (s *Server) handleTelegramBind(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if decodeJSON(r, &req) != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if !s.telegramCredentials(w, r, req.Password, req.Code) {
		return
	}
	t := s.telegram
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.load(r.Context()) != nil {
		writeError(w, 500, "读取配置失败")
		return
	}
	if r.Method == http.MethodDelete {
		old := t.cfg
		t.cfg.BoundUserID = 0
		t.cfg.BoundUsername = ""
		if t.save(r.Context()) != nil {
			t.cfg = old
			writeError(w, 500, "解除绑定失败")
			return
		}
		t.revoke()
		s.audit(r, "telegram.unbind", "telegram", "")
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if !t.cfg.Enabled || t.cfg.BotUsername == "" {
		writeError(w, 400, "请先启用 Telegram Bot")
		return
	}
	nonce, err := cryptox.RandomToken(24)
	if err != nil {
		writeError(w, 500, "生成绑定码失败")
		return
	}
	expires := time.Now().Add(5 * time.Minute)
	t.bindings = map[string]telegramBinding{nonce: {UserID: userFrom(r.Context()).ID, Expires: expires}}
	s.audit(r, "telegram.bind_requested", "telegram", "")
	writeJSON(w, 200, map[string]any{"url": "https://t.me/" + t.cfg.BotUsername + "?start=" + nonce, "expires_at": expires})
}

func validateTelegramInit(raw, token string, now time.Time) (int64, string, error) {
	fail := errors.New("invalid Telegram data")
	if len(raw) > 16384 {
		return 0, "", fail
	}
	v, err := url.ParseQuery(raw)
	if err != nil {
		return 0, "", fail
	}
	keys := make([]string, 0, len(v))
	for k, values := range v {
		if len(values) != 1 {
			return 0, "", fail
		}
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+v.Get(k))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	hash, err := hex.DecodeString(v.Get("hash"))
	if err != nil || !hmac.Equal(hash, mac.Sum(nil)) {
		return 0, "", fail
	}
	stamp, err := strconv.ParseInt(v.Get("auth_date"), 10, 64)
	if err != nil || stamp > now.Unix() || now.Unix()-stamp > 300 {
		return 0, "", fail
	}
	var user struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	}
	if json.Unmarshal([]byte(v.Get("user")), &user) != nil || user.ID <= 0 || user.IsBot {
		return 0, "", fail
	}
	if chatType := v.Get("chat_type"); chatType != "" && chatType != "private" && chatType != "sender" {
		return 0, "", fail
	}
	return user.ID, hex.EncodeToString(hash), nil
}
func (s *Server) handleTelegramAuth(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InitData string `json:"init_data"`
	}
	if decodeJSON(r, &req) != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	t := s.telegram
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.load(r.Context()) != nil || !t.cfg.Enabled || t.cfg.BoundUserID <= 0 {
		writeError(w, 401, "Telegram 未启用或未绑定")
		return
	}
	now := time.Now()
	actor, hash, err := validateTelegramInit(req.InitData, t.cfg.Token, now)
	if err != nil || actor != t.cfg.BoundUserID {
		writeError(w, 401, "Telegram 身份验证失败，请重新打开面板")
		return
	}
	for h, exp := range t.used {
		if !exp.After(now) {
			delete(t.used, h)
		}
	}
	for k, v := range t.sessions {
		if !v.Expires.After(now) {
			delete(t.sessions, k)
		}
	}
	if _, exists := t.used[hash]; exists {
		writeError(w, 401, "登录数据已使用，请从 Bot 重新打开面板")
		return
	}
	owner, err := s.svc.Store().UserByID(r.Context(), t.cfg.UserID)
	if err != nil || owner.Locked(now) {
		writeError(w, 401, "管理员不可用")
		return
	}
	token, err := cryptox.RandomToken(32)
	if err != nil {
		writeError(w, 500, "创建会话失败")
		return
	}
	expires := now.Add(30 * time.Minute)
	t.used[hash] = now.Add(5 * time.Minute)
	t.sessions[token] = telegramSession{UserID: t.cfg.UserID, Actor: actor, Expires: expires, Credentials: telegramCredentialsFingerprint(owner)}
	writeJSON(w, 200, map[string]any{"token": token, "expires_at": expires})
}

// Deny by default: Telegram identities never inherit the browser admin perimeter.
func telegramRouteAllowed(method, path string) bool {
	if path == "/api/telegram/machine-draft" {
		return method == http.MethodGet
	}
	p := strings.Split(strings.TrimPrefix(path, "/api/"), "/")
	if len(p) == 1 {
		switch p[0] {
		case "status", "traffic", "quotas", "metrics":
			return method == "GET"
		case "nodes", "routes":
			return method == "GET" || method == "POST"
		}
		return false
	}
	if len(p) == 2 && p[0] == "traffic" && p[1] == "daily" {
		return method == "GET"
	}
	if len(p) == 2 && (p[0] == "nodes" || p[0] == "routes") && p[1] == "order" {
		return method == http.MethodPut
	}
	positive := func(s string) bool { n, e := strconv.ParseInt(s, 10, 64); return e == nil && n > 0 }
	if (p[0] == "nodes" || p[0] == "routes") && positive(p[1]) {
		if len(p) == 2 {
			return method == "GET" || method == "PUT" || method == "DELETE"
		}
		if len(p) == 3 {
			if p[0] == "nodes" {
				return p[2] == "probe" && method == "POST"
			}
			switch p[2] {
			case "apply", "verify", "stop":
				return method == "POST"
			case "traffic":
				return method == "GET"
			}
		}
		return len(p) == 4 && p[0] == "nodes" && p[2] == "singbox" && p[3] == "scan" && method == "GET"
	}
	if p[0] == "singbox" && p[1] == "users" {
		if len(p) == 2 {
			return method == "GET" || method == "POST"
		}
		if !positive(p[2]) {
			return false
		}
		if len(p) == 3 {
			return method == "PUT" || method == "DELETE"
		}
		if len(p) == 4 {
			switch p[3] {
			case "export":
				return method == "GET"
			case "start", "stop", "top-up", "reduce", "reset", "expiry", "adopt":
				return method == "POST"
			}
		}
	}
	return false
}

const telegramActorKey contextKey = "telegram_actor"

func (s *Server) authenticateTelegram(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if !telegramRouteAllowed(r.Method, r.URL.Path) {
		writeError(w, 403, "Telegram 不允许此操作")
		return
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeError(w, 401, "无效认证格式")
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	t := s.telegram
	t.mu.Lock()
	session, ok := t.sessions[token]
	cfg := t.cfg
	t.mu.Unlock()
	if !ok || !session.Expires.After(time.Now()) || !cfg.Enabled || cfg.BoundUserID != session.Actor || cfg.UserID != session.UserID {
		writeError(w, 401, "Telegram 会话已失效，请重新打开")
		return
	}
	user, err := s.svc.Store().UserByID(r.Context(), session.UserID)
	if err != nil || user.Locked(time.Now()) || telegramCredentialsFingerprint(user) != session.Credentials {
		writeError(w, 401, "管理员已失效")
		return
	}
	ctx := context.WithValue(r.Context(), userContextKey, user)
	ctx = context.WithValue(ctx, telegramActorKey, session.Actor)
	w.Header().Set("Cache-Control", "no-store")
	next.ServeHTTP(w, r.WithContext(ctx))
}

func telegramCredentialsFingerprint(u *store.User) [32]byte {
	return sha256.Sum256([]byte(u.PasswordHash + "\x00" + u.TOTPSecret + "\x00" + strconv.FormatBool(u.TOTPEnrolled)))
}
