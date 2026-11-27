package probe

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/kukumi1/fluxlite/internal/store"
)

// DiscoveryKeySetting holds the key new agents register with.
const DiscoveryKeySetting = "auto_discovery_key"

const (
	maxBody = 1 << 20
	// readIdle closes a report connection that has gone quiet; agents report
	// every few seconds and ping every thirty.
	readIdle = 45 * time.Second
	// pullWait is how long an agent's poll for work is held open.
	pullWait = 25 * time.Second
)

// Routes are the endpoints Komari's agent calls, mounted under /api.
func (h *Hub) Routes(r chi.Router) {
	r.Post("/clients/register", h.handleRegister)
	r.Post("/clients/uploadBasicInfo", h.authed(h.handleBasicInfoV1))
	r.Get("/clients/report", h.authed(h.handleReportV1))
	r.Get("/clients/v2/rpc", h.authed(h.handleSocketV2))
	r.Post("/clients/v2/rpc", h.authed(h.handlePostV2))
	// The panel never sends commands, but an agent finishing one it was sent
	// elsewhere should not retry forever.
	r.Post("/clients/task/result", h.authed(func(w http.ResponseWriter, _ *http.Request, _ *store.ProbeClient) {
		writeStatus(w, http.StatusOK, "success", "")
	}))
}

func writeStatus(w http.ResponseWriter, code int, status, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "message": message})
}

// authed resolves the agent's token, which Komari's agent sends in the query.
func (h *Hub) authed(next func(http.ResponseWriter, *http.Request, *store.ProbeClient)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("token")
		if token == "" {
			token = r.URL.Query().Get("Authorization")
		}
		var c *store.ProbeClient
		var err error
		if token != "" {
			c, err = h.store.ProbeClientByToken(r.Context(), token)
		}
		if token == "" || errors.Is(err, store.ErrNotFound) {
			writeStatus(w, http.StatusUnauthorized, "error", "Unauthorized.")
			return
		}
		if err != nil {
			h.log.Error("find probe client", "error", err)
			writeStatus(w, http.StatusInternalServerError, "error", "Internal error.")
			return
		}
		next(w, r, c)
	}
}

func (h *Hub) handleRegister(w http.ResponseWriter, r *http.Request) {
	key, err := h.store.ProbeSetting(r.Context(), DiscoveryKeySetting)
	if err != nil {
		writeStatus(w, http.StatusInternalServerError, "error", "Internal error.")
		return
	}
	given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(key) < 12 || subtle.ConstantTimeCompare([]byte(given), []byte(key)) != 1 {
		writeStatus(w, http.StatusUnauthorized, "error", "Invalid AutoDiscovery Key")
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "新机器"
	}
	c := store.ProbeClient{UUID: uuid.NewString(), Token: rand.Text()[:22], Name: name}
	if err := h.store.CreateProbeClient(r.Context(), c); err != nil {
		h.log.Error("register probe client", "error", err)
		writeStatus(w, http.StatusInternalServerError, "error", "Internal error.")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "message": "", "data": map[string]string{"uuid": c.UUID, "token": c.Token}})
}

// readBody reads a request body, which v2 agents gzip.
func readBody(r *http.Request) ([]byte, error) {
	var body io.Reader = http.MaxBytesReader(nil, r.Body, maxBody)
	if strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(body)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		body = io.LimitReader(gz, maxBody)
	}
	return io.ReadAll(body)
}

// saveInfo records an agent's description of its machine. With no address
// given, the one it connected from is used, as Komari does.
func (h *Hub) saveInfo(ctx context.Context, r *http.Request, c *store.ProbeClient, raw json.RawMessage) error {
	var info map[string]any
	if err := json.Unmarshal(raw, &info); err != nil {
		return err
	}
	if s, _ := info["ipv4"].(string); s == "" {
		if s6, _ := info["ipv6"].(string); s6 == "" {
			if ip := net.ParseIP(remoteHost(r)); ip != nil {
				if ip.To4() != nil {
					info["ipv4"] = ip.String()
				} else {
					info["ipv6"] = ip.String()
				}
			}
		}
	}
	b, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return h.store.SetProbeInfo(ctx, c.UUID, b)
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (h *Hub) handleBasicInfoV1(w http.ResponseWriter, r *http.Request, c *store.ProbeClient) {
	body, err := readBody(r)
	if err == nil {
		err = h.saveInfo(r.Context(), r, c, body)
	}
	if err != nil {
		writeStatus(w, http.StatusBadRequest, "error", "Invalid basic info.")
		return
	}
	writeStatus(w, http.StatusOK, "success", "")
}

// accept upgrades a report connection. The server's write timeout covers the
// whole of a request, which a connection held open for days outlives.
func accept(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return nil, err
	}
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return nil, err
	}
	// Agents are not browsers and send no Origin; there is no cookie here
	// for a cross-site page to borrow.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionNoContextTakeover})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(maxBody)
	return conn, nil
}

// serve reads a report connection until it closes, handing each message to
// handle; any reply handle returns is sent back.
func (h *Hub) serve(r *http.Request, conn *websocket.Conn, c *store.ProbeClient, v2 bool, handle func([]byte) []byte) {
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	agent := &agentConn{v2: v2, send: func(ctx context.Context, msg []byte) error { return conn.Write(ctx, websocket.MessageText, msg) }}
	disconnect := h.connect(c.UUID, agent)
	defer disconnect()
	for {
		readCtx, done := context.WithTimeout(ctx, readIdle)
		_, msg, err := conn.Read(readCtx)
		done()
		if err != nil {
			return
		}
		if reply := handle(msg); reply != nil {
			if err := conn.Write(ctx, websocket.MessageText, reply); err != nil {
				return
			}
		}
	}
}

// pingParams is a latency result as v2 sends it; v1 adds a type.
type pingParams struct {
	Type   string  `json:"type"`
	TaskID int64   `json:"task_id"`
	Value  float64 `json:"value"`
}

func (h *Hub) handleReportV1(w http.ResponseWriter, r *http.Request, c *store.ProbeClient) {
	conn, err := accept(w, r)
	if err != nil {
		return
	}
	h.serve(r, conn, c, false, func(msg []byte) []byte {
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(msg, &kind) != nil {
			return nil
		}
		switch kind.Type {
		case "":
			var rep Report
			if json.Unmarshal(msg, &rep) == nil && rep.valid() {
				h.record(c.UUID, rep, time.Now())
			}
		case "ping_result":
			var p pingParams
			if json.Unmarshal(msg, &p) == nil {
				h.recordPing(c.UUID, p.TaskID, p.Value, time.Now())
			}
		}
		return nil
	})
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func rpcReply(id json.RawMessage, result any) []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return b
}

func rpcError(id json.RawMessage, code int, message string) []byte {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
	return b
}

// call handles one v2 message from an agent. Notifications get no reply;
// requests get a result or an error.
func (h *Hub) call(ctx context.Context, r *http.Request, c *store.ProbeClient, req rpcRequest) []byte {
	var err error
	var result any = map[string]string{"status": "success"}
	switch req.Method {
	case "agent.report":
		var p struct {
			Report Report `json:"report"`
		}
		if err = json.Unmarshal(req.Params, &p); err == nil && p.Report.valid() {
			h.record(c.UUID, p.Report, time.Now())
		}
	case "agent.pingResult":
		var p pingParams
		if err = json.Unmarshal(req.Params, &p); err == nil {
			h.recordPing(c.UUID, p.TaskID, p.Value, time.Now())
		}
	case "agent.basicInfo":
		var p struct {
			Info json.RawMessage `json:"info"`
		}
		if err = json.Unmarshal(req.Params, &p); err == nil {
			err = h.saveInfo(ctx, r, c, p.Info)
		}
	case "agent.pull":
		// Work is only ever sent down an open connection; an agent polling
		// instead is told there is none, after the wait it expects.
		select {
		case <-ctx.Done():
		case <-time.After(pullWait):
		}
		result = map[string]any{"events": []any{}}
	default:
		if len(req.ID) == 0 {
			return nil
		}
		return rpcError(req.ID, -32601, "Method not found")
	}
	if len(req.ID) == 0 {
		return nil
	}
	if err != nil {
		return rpcError(req.ID, -32602, "Invalid params")
	}
	return rpcReply(req.ID, result)
}

func (h *Hub) handleSocketV2(w http.ResponseWriter, r *http.Request, c *store.ProbeClient) {
	conn, err := accept(w, r)
	if err != nil {
		return
	}
	h.serve(r, conn, c, true, func(msg []byte) []byte {
		var req rpcRequest
		if json.Unmarshal(msg, &req) != nil {
			return nil
		}
		return h.call(context.WithoutCancel(r.Context()), r, c, req)
	})
}

func (h *Hub) handlePostV2(w http.ResponseWriter, r *http.Request, c *store.ProbeClient) {
	body, err := readBody(r)
	var req rpcRequest
	if err == nil {
		err = json.Unmarshal(body, &req)
	}
	if err != nil || req.JSONRPC != "2.0" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(rpcError(nil, -32700, "Parse error"))
		return
	}
	reply := h.call(r.Context(), r, c, req)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if reply != nil {
		_, _ = w.Write(reply)
	}
}
