package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/kukumi1/fluxlite/internal/model"
)

const (
	// consoleIdleTimeout closes a terminal nobody is typing into. A forgotten
	// tab is an open root shell; it should not outlive the attention that
	// opened it.
	consoleIdleTimeout = 30 * time.Minute

	// consolePingInterval keeps intermediaries from reaping a quiet
	// connection. A reverse proxy sees an idle WebSocket as a dead one.
	consolePingInterval = 30 * time.Second

	consoleReadLimit = 1 << 20
)

// requireConsoleUnlocked gates everything terminal-related behind a session
// that has re-proven the password.
//
// A valid session is enough to read the panel and change forwarding. It is
// deliberately not enough to obtain a root shell on eight machines: a stolen
// cookie should stop at the first of those, not the second.
func (s *Server) requireConsoleUnlocked(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		unlocked, err := s.svc.Store().ConsoleUnlocked(r.Context(), cookie.Value)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !unlocked {
			writeError(w, http.StatusForbidden, "终端未解锁，请先验证身份")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type consoleUnlockRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

func (s *Server) handleConsoleUnlock(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	var req consoleUnlockRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	user := userFrom(r.Context())
	if err := s.auth.VerifyPasswordAndCode(r.Context(), user.ID, req.Password, trimmed(req.Code)); err != nil {
		s.audit(r, "console.unlock_failed", user.Username, err.Error())
		writeError(w, statusForError(err), err.Error())
		return
	}
	if err := s.svc.Store().MarkConsoleUnlocked(r.Context(), cookie.Value, time.Now().UTC()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "console.unlock", user.Username, "terminal access unlocked for this session")
	writeJSON(w, http.StatusOK, map[string]bool{"unlocked": true})
}

func (s *Server) handleConsoleStatus(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	unlocked, err := s.svc.Store().ConsoleUnlocked(r.Context(), cookie.Value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"unlocked": unlocked})
}

// consoleControl is what the browser sends as a text frame. Keystrokes travel
// as binary frames instead, so ordinary typing costs no encoding.
type consoleControl struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// handleConsole bridges a WebSocket to an interactive shell on a node.
//
// The Origin check is not optional. WebSocket handshakes are exempt from the
// same-origin policy that protects fetch: without it, any page the operator
// visits could open this endpoint with their cookie attached and hold a root
// shell on every managed machine. coder/websocket rejects a mismatched Origin
// by default, and that default is the entire defence — do not add
// InsecureSkipVerify here.
func (s *Server) handleConsole(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		// Accept has already written a response describing the refusal.
		s.log.Warn("console websocket refused", "error", err)
		return
	}
	conn.SetReadLimit(consoleReadLimit)
	defer conn.CloseNow()

	user := userFrom(r.Context())
	client, node, err := s.svc.DialConsole(r.Context(), id)
	if err != nil {
		s.closeWithReason(conn, "连接节点失败："+err.Error())
		return
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		s.closeWithReason(conn, "打开会话失败："+err.Error())
		return
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		s.closeWithReason(conn, "打开输入通道失败："+err.Error())
		return
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		s.closeWithReason(conn, "打开输出通道失败："+err.Error())
		return
	}
	// stderr 不单独接管：申请了 PTY 之后远端会把两个流并进同一个终端，
	// stdout 这条管道拿到的就是全部输出。
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 38400, ssh.TTY_OP_OSPEED: 38400}
	if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		s.closeWithReason(conn, "申请终端失败："+err.Error())
		return
	}
	if err := session.Shell(); err != nil {
		s.closeWithReason(conn, "启动 shell 失败："+err.Error())
		return
	}

	opened := time.Now()
	s.audit(r, "console.open", node.Name, "interactive shell opened")
	defer func() {
		s.audit(r, "console.close", node.Name,
			"session lasted "+time.Since(opened).Round(time.Second).String())
	}()
	s.log.Info("console opened", "node", node.Name, "user", user.Username)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 空闲计时只由用户的输入推进。仅有输出不算活跃 —— 一个刷个不停的
	// tail -f 会让一个没人看的 root shell 永远不过期。
	activity := make(chan struct{}, 1)
	go s.consoleIdleWatch(ctx, cancel, activity)
	go s.consolePing(ctx, conn)

	// 节点 → 浏览器
	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					s.log.Debug("console read", "node", node.Name, "error", err)
				}
				return
			}
		}
	}()

	// 浏览器 → 节点
	for {
		kind, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		select {
		case activity <- struct{}{}:
		default:
		}

		if kind == websocket.MessageText {
			var control consoleControl
			if json.Unmarshal(data, &control) == nil && control.Cols > 0 && control.Rows > 0 {
				if werr := session.WindowChange(int(control.Rows), int(control.Cols)); werr != nil {
					s.log.Debug("console resize", "node", node.Name, "error", werr)
				}
			}
			continue
		}
		if _, err := stdin.Write(data); err != nil {
			return
		}
	}
}

func (s *Server) consoleIdleWatch(ctx context.Context, cancel context.CancelFunc, activity <-chan struct{}) {
	timer := time.NewTimer(consoleIdleTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(consoleIdleTimeout)
		case <-timer.C:
			cancel()
			return
		}
	}
}

func (s *Server) consolePing(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(consolePingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

// closeWithReason tells the browser why the terminal never opened. Closing
// silently would leave the tab staring at a shell that never answers.
func (s *Server) closeWithReason(conn *websocket.Conn, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, []byte(reason))
	_ = conn.Close(websocket.StatusInternalError, truncateReason(reason))
}

// truncateReason fits a message into a close frame, which allows 123 bytes and
// must stay valid UTF-8. These messages are Chinese, so cutting at a byte
// offset would split a character and make the frame itself invalid — the
// browser would then report a protocol error instead of the reason.
func truncateReason(reason string) string {
	const limit = 100
	if len(reason) <= limit {
		return reason
	}
	out := reason[:limit]
	for len(out) > 0 && !utf8.ValidString(out) {
		out = out[:len(out)-1]
	}
	return out
}

func (s *Server) handleListConsoleCommands(w http.ResponseWriter, r *http.Request) {
	commands, err := s.svc.Store().ListConsoleCommands(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, commands)
}

func (s *Server) handleCreateConsoleCommand(w http.ResponseWriter, r *http.Request) {
	var req model.ConsoleCommand
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Name = trimmed(req.Name)
	req.Command = strings.TrimSpace(req.Command)
	if req.Name == "" || req.Command == "" {
		writeError(w, http.StatusBadRequest, "名称与命令都不能为空")
		return
	}
	if err := s.svc.Store().CreateConsoleCommand(r.Context(), &req); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "console.command_create", req.Name, req.Command)
	writeJSON(w, http.StatusCreated, &req)
}

func (s *Server) handleDeleteConsoleCommand(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.svc.Store().DeleteConsoleCommand(r.Context(), id); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "console.command_delete", strconv.FormatInt(id, 10), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
