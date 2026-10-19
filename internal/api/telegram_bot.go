package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

// No Telegram response or transport error is exposed: both may contain the bot token.
var telegramHTTPClient = &http.Client{Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// telegramRateLimitError carries only a validated delay, never Telegram's description.
type telegramRateLimitError struct{ RetryAfter time.Duration }

func (e *telegramRateLimitError) Error() string { return "Telegram rate limited the request" }

func telegramCall(ctx context.Context, token, method string, payload, result any) error {
	switch method {
	case "getMe", "getUpdates", "sendMessage", "answerCallbackQuery":
	default:
		return errors.New("unsupported Telegram method")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("cannot encode Telegram request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return errors.New("cannot create Telegram request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := telegramHTTPClient.Do(req)
	if err != nil {
		return errors.New("Telegram connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		var envelope struct {
			Parameters struct {
				RetryAfter int64 `json:"retry_after"`
			} `json:"parameters"`
		}
		err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&envelope)
		delay := time.Duration(0)
		// Longer limits are not retried here: never sleep unboundedly or retry earlier than requested.
		if err == nil && envelope.Parameters.RetryAfter >= 1 && envelope.Parameters.RetryAfter <= 60 {
			delay = time.Duration(envelope.Parameters.RetryAfter) * time.Second
		}
		return &telegramRateLimitError{RetryAfter: delay}
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("Telegram request rejected")
	}
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&envelope); err != nil || !envelope.OK {
		return errors.New("invalid Telegram response")
	}
	if result != nil {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return errors.New("invalid Telegram result")
		}
	}
	return nil
}

type telegramBotUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsBot    bool   `json:"is_bot"`
}
type telegramBotChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
type telegramBotMessage struct {
	From telegramBotUser `json:"from"`
	Chat telegramBotChat `json:"chat"`
	Text string          `json:"text"`
}
type telegramBotCallback struct {
	ID      string              `json:"id"`
	From    telegramBotUser     `json:"from"`
	Message *telegramBotMessage `json:"message"`
	Data    string              `json:"data"`
}
type telegramUpdate struct {
	ID       int64                `json:"update_id"`
	Message  *telegramBotMessage  `json:"message"`
	Callback *telegramBotCallback `json:"callback_query"`
}

func telegramWait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// RunTelegram owns one long-poll loop. Updates are checkpointed before handling,
// giving binding operations at-most-once semantics across process restarts.
func (s *Server) RunTelegram(ctx context.Context) {
	if s.telegram == nil {
		return
	}
	for ctx.Err() == nil {
		s.telegram.mu.Lock()
		loadErr := s.telegram.load(ctx)
		cfg := s.telegram.cfg
		s.telegram.mu.Unlock()
		if loadErr != nil {
			s.telegram.mu.Lock()
			s.telegram.status = "读取配置失败，稍后重试"
			s.telegram.mu.Unlock()
		}
		if loadErr != nil || !cfg.Enabled || cfg.Token == "" {
			if !telegramWait(ctx, time.Second) {
				return
			}
			continue
		}
		var updates []telegramUpdate
		err := telegramCall(ctx, cfg.Token, "getUpdates", map[string]any{"offset": cfg.Offset, "timeout": 25, "limit": 50, "allowed_updates": []string{"message", "callback_query"}}, &updates)
		s.telegram.mu.Lock()
		if s.telegram.cfg.Enabled && s.telegram.cfg.Token == cfg.Token {
			if err != nil {
				s.telegram.status = "连接失败，请检查 Token、网络或 Bot 是否被其他程序占用"
			} else {
				s.telegram.status = "运行中"
			}
		}
		s.telegram.mu.Unlock()
		if err != nil {
			if !telegramWait(ctx, 5*time.Second) {
				return
			}
			continue
		}
		for _, update := range updates {
			s.telegram.mu.Lock()
			if !s.telegram.cfg.Enabled || s.telegram.cfg.Token != cfg.Token {
				s.telegram.mu.Unlock()
				break
			}
			if update.ID < s.telegram.cfg.Offset {
				s.telegram.mu.Unlock()
				continue
			}
			oldOffset := s.telegram.cfg.Offset
			s.telegram.cfg.Offset = update.ID + 1
			err := s.telegram.save(ctx)
			if err != nil {
				s.telegram.cfg.Offset = oldOffset
			}
			current := s.telegram.cfg
			s.telegram.mu.Unlock()
			if err != nil {
				break
			}
			s.processTelegramUpdate(ctx, current, update)
		}
		// Also bounds request rates for an empty immediate response.
		if !telegramWait(ctx, 100*time.Millisecond) {
			return
		}
	}
}

func (s *Server) processTelegramUpdate(ctx context.Context, cfg telegramConfig, update telegramUpdate) {
	var from telegramBotUser
	var chat telegramBotChat
	var command string
	if update.Message != nil {
		from, chat, command = update.Message.From, update.Message.Chat, update.Message.Text
	} else if update.Callback != nil && update.Callback.Message != nil {
		from, chat, command = update.Callback.From, update.Callback.Message.Chat, update.Callback.Data
	} else {
		return
	}
	if from.IsBot || from.ID <= 0 || chat.Type != "private" || chat.ID != from.ID {
		return
	}
	s.telegram.mu.Lock()
	if !s.telegram.cfg.Enabled || s.telegram.cfg.Token != cfg.Token {
		s.telegram.mu.Unlock()
		return
	}
	owner, err := s.svc.Store().UserByID(ctx, s.telegram.cfg.UserID)
	if err != nil || owner.Locked(time.Now()) {
		s.telegram.mu.Unlock()
		return
	}
	fields := strings.Fields(command)
	boundNow := false
	if update.Message != nil && len(fields) == 2 && fields[0] == "/start" {
		nonce := strings.TrimPrefix(fields[1], "bind_")
		if binding, ok := s.telegram.bindings[nonce]; ok && binding.UserID == s.telegram.cfg.UserID && time.Now().Before(binding.Expires) {
			old := s.telegram.cfg
			s.telegram.cfg.BoundUserID, s.telegram.cfg.BoundUsername = from.ID, from.Username
			if err := s.telegram.save(ctx); err != nil {
				s.telegram.cfg = old
				s.telegram.mu.Unlock()
				return
			}
			s.telegram.revoke()
			boundNow = true
		}
	}
	cfg = s.telegram.cfg
	authorized := cfg.BoundUserID == from.ID
	s.telegram.mu.Unlock()
	if !authorized {
		return
	}
	if boundNow {
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditWriteTimeout)
		err := s.svc.Store().AppendAudit(auditCtx, &model.AuditLog{Actor: fmt.Sprintf("telegram:%d", from.ID), Action: "telegram.bind", Target: fmt.Sprint(cfg.UserID), Detail: "Telegram private account bound"})
		cancel()
		if err != nil {
			s.log.Error("write Telegram binding audit")
		}
	}
	if update.Callback != nil {
		if err := telegramCall(ctx, cfg.Token, "answerCallbackQuery", map[string]any{"callback_query_id": update.Callback.ID}, nil); err != nil {
			s.log.Debug("Telegram callback acknowledgement failed")
		}
	}
	if s.telegramDirect(ctx, cfg, from, command, update.Callback != nil) {
		return
	}
	page := "dashboard"
	switch command {
	case "/routes", "routes":
		page = "routes"
	case "/nodes", "nodes":
		page = "nodes"
	case "/singbox", "singbox":
		page = "singbox"
	}
	text, err := s.telegramView(ctx, page)
	if err != nil {
		text = "暂时无法读取面板数据，请稍后重试。"
	}
	if boundNow {
		text = "管理员绑定成功。\n\n" + text
	}
	// Check revocation again immediately before disclosing a panel snapshot.
	s.telegram.mu.Lock()
	active := s.telegram.cfg.Enabled && s.telegram.cfg.Token == cfg.Token && s.telegram.cfg.BoundUserID == from.ID
	s.telegram.mu.Unlock()
	if !active {
		return
	}
	if err := s.sendTelegramMessage(ctx, cfg, chat.ID, text, page); err != nil {
		s.log.Debug("Telegram message delivery failed")
	}
}

func telegramMenu(panelURL, page string) map[string]any {
	callback := func(label, key string) map[string]any { return map[string]any{"text": label, "callback_data": key} }
	return map[string]any{"inline_keyboard": [][]map[string]any{
		{callback("整体仪表盘", "dashboard"), callback("链路", "routes")},
		{callback("机器", "nodes"), callback("节点", "singbox")},
		{callback("链路流量", "traffic:0")},
		{{"text": "打开内嵌面板 · 查看与管理", "web_app": map[string]string{"url": strings.TrimRight(panelURL, "/") + "/telegram?tab=" + page}}},
	}}
}

func (s *Server) telegramView(ctx context.Context, page string) (string, error) {
	st := s.svc.Store()
	var b strings.Builder
	switch page {
	case "routes":
		routes, err := st.ListRoutes(ctx)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "链路（%d）\n", len(routes))
		for i, r := range routes {
			if i == 15 {
				b.WriteString("更多链路请打开内嵌面板。\n")
				break
			}
			state := "已停止"
			if r.Enabled {
				state = "已启用"
			}
			fmt.Fprintf(&b, "• %s · %s · %s\n", telegramDisplayName(r.Name), r.Protocol, state)
		}
	case "nodes":
		nodes, err := st.ListNodes(ctx)
		if err != nil {
			return "", err
		}
		metrics, err := st.ListNodeMetrics(ctx)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "机器（%d）\n", len(nodes))
		for i, n := range nodes {
			if i == 10 {
				b.WriteString("更多机器请打开内嵌面板。\n")
				break
			}
			fmt.Fprintf(&b, "• %s · %s\n", telegramDisplayName(n.Name), telegramNodeStatus(n.Status))
			if m := metrics[n.ID]; m != nil {
				fmt.Fprintf(&b, "  CPU %s · 内存 %s · 磁盘 %s\n", telegramCPUPercent(m.CPUPercent), telegramPercent(m.MemUsed, m.MemTotal), telegramPercent(m.DiskUsed, m.DiskTotal))
			}
		}
	case "singbox":
		nodes, err := st.ListSingBoxUsers(ctx)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "节点（%d）\n", len(nodes))
		for i, n := range nodes {
			if i == 15 {
				b.WriteString("更多节点请打开内嵌面板。\n")
				break
			}
			state := "已关闭"
			if n.Enabled {
				state = "已启用"
			}
			fmt.Fprintf(&b, "• %s · %s · %s\n", telegramDisplayName(n.Name), n.Protocol, state)
		}
	default:
		nodes, err := st.ListNodes(ctx)
		if err != nil {
			return "", err
		}
		routes, err := st.ListRoutes(ctx)
		if err != nil {
			return "", err
		}
		users, err := st.ListSingBoxUsers(ctx)
		if err != nil {
			return "", err
		}
		traffic, err := st.RouteTraffic(ctx)
		if err != nil {
			return "", err
		}
		online, enabled := 0, 0
		for _, n := range nodes {
			if n.Status == model.StatusOnline {
				online++
			}
		}
		for _, r := range routes {
			if r.Enabled {
				enabled++
			}
		}
		var in, out int64
		for _, v := range traffic {
			in += v.BytesIn
			out += v.BytesOut
		}
		fmt.Fprintf(&b, "Fluxlite 整体仪表盘\n机器在线：%d / %d\n链路启用：%d / %d\n节点总数：%d\n累计入向：%.2f GB\n累计出向：%.2f GB\n流量为已采集数据，未采集不代表零流量。\n", online, len(nodes), enabled, len(routes), len(users), float64(in)/(1<<30), float64(out)/(1<<30))
	}
	b.WriteString("\n使用下方菜单管理；SSH 密码和私钥仅在安全内嵌表单填写，不要发送到聊天。")
	return telegramMessageText(b.String()), nil
}

func telegramPercent(used, total *int64) string {
	if used == nil || total == nil || *total <= 0 {
		return "未知"
	}
	return fmt.Sprintf("%.0f%%", float64(*used)*100/float64(*total))
}
func telegramCPUPercent(cpu *float64) string {
	if cpu == nil {
		return "未知"
	}
	return fmt.Sprintf("%.0f%%", *cpu)
}
func telegramNodeStatus(status model.NodeStatus) string {
	switch status {
	case model.StatusOnline:
		return "在线"
	case model.StatusOffline:
		return "离线"
	default:
		return "状态未知"
	}
}

// RunTelegram is a single worker; pacing before every send also spaces adjacent updates.
// Only explicit 429s retry: retrying uncertain transport failures could duplicate messages.
func (s *Server) sendTelegramMessage(ctx context.Context, cfg telegramConfig, chatID int64, text, page string) error {
	return s.sendTelegramReply(ctx, cfg, chatID, text, telegramMenu(cfg.PanelURL, page))
}

func (s *Server) sendTelegramReply(ctx context.Context, cfg telegramConfig, chatID int64, text string, keyboard map[string]any) error {
	delay := time.Second
	for attempt := 0; attempt < 3; attempt++ {
		if !telegramWait(ctx, delay) {
			return ctx.Err()
		}
		s.telegram.mu.Lock()
		active := s.telegram.cfg.Enabled && s.telegram.cfg.Token == cfg.Token && s.telegram.cfg.UserID == cfg.UserID && s.telegram.cfg.BoundUserID == chatID && s.telegram.cfg.PanelURL == cfg.PanelURL
		s.telegram.mu.Unlock()
		if !active {
			return errors.New("Telegram authorization revoked")
		}
		owner, err := s.svc.Store().UserByID(ctx, cfg.UserID)
		if err != nil || owner.Locked(time.Now()) {
			return errors.New("Telegram owner unavailable")
		}
		err = telegramCall(ctx, cfg.Token, "sendMessage", map[string]any{"chat_id": chatID, "text": telegramMessageText(text), "protect_content": true, "reply_markup": keyboard}, nil)
		if err == nil {
			return nil
		}
		var limited *telegramRateLimitError
		if !errors.As(err, &limited) || limited.RetryAfter <= 0 || attempt == 2 {
			return err
		}
		delay = limited.RetryAfter
	}
	return errors.New("Telegram message delivery failed")
}

func telegramDisplayName(name string) string {
	chars := []rune(strings.Join(strings.Fields(name), " "))
	if len(chars) > 80 {
		return string(chars[:79]) + "…"
	}
	return string(chars)
}

// Telegram's 4096 limit is conservatively measured in UTF-16 units; leave space
// for the truncation notice and never split a multibyte character or surrogate pair.
func telegramMessageText(text string) string {
	units := 0
	for pos, r := range text {
		width := 1
		if r > 0xffff {
			width = 2
		}
		if units+width > 3900 {
			return text[:pos] + "\n…更多内容请打开内嵌面板。"
		}
		units += width
	}
	return text
}
