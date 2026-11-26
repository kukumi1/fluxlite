package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/kukumi1/fluxlite/internal/service"
)

// handleAlertSettings returns the notification choices, and whether there is
// a bound Telegram account to deliver them to.
func (s *Server) handleAlertSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.svc.AlertSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取通知设置失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": cfg, "telegram_bound": s.telegramBound(r)})
}

func (s *Server) handleSaveAlertSettings(w http.ResponseWriter, r *http.Request) {
	var cfg service.AlertSettings
	if err := decodeJSON(r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.svc.SetAlertSettings(r.Context(), cfg); err != nil {
		if errors.Is(err, service.ErrAlertSettings) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "保存通知设置失败")
		return
	}
	s.audit(r, "alerts.settings", "通知", "")
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleTestAlert(w http.ResponseWriter, r *http.Request) {
	if !s.telegramBound(r) {
		writeError(w, http.StatusConflict, "还没有启用 Telegram 机器人或绑定账号，请先到个人中心设置")
		return
	}
	if err := s.NotifyTelegram(r.Context(), "🔔 测试消息：fluxlite 的提醒会发到这里"); err != nil {
		writeError(w, http.StatusBadGateway, "发送失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) telegramBound(r *http.Request) bool {
	s.telegram.mu.Lock()
	defer s.telegram.mu.Unlock()
	if s.telegram.load(r.Context()) != nil {
		return false
	}
	cfg := s.telegram.cfg
	return cfg.Enabled && cfg.Token != "" && cfg.BoundUserID > 0
}

func (s *Server) handleLatencyOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := s.svc.LatencyOverview(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取线路质量失败")
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (s *Server) handleSaveLatencyTargets(w http.ResponseWriter, r *http.Request) {
	var targets []service.LatencyTarget
	if err := decodeJSON(r, &targets); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.svc.SetLatencyTargets(r.Context(), targets); err != nil {
		if errors.Is(err, service.ErrLatencyTargets) {
			writeError(w, http.StatusBadRequest, "测试点设置无效：名称不能重复，地址和端口要有效，最多启用 12 个")
			return
		}
		writeError(w, http.StatusInternalServerError, "保存测试点失败")
		return
	}
	s.audit(r, "latency.targets", "测试点", strconv.Itoa(len(targets)))
	writeJSON(w, http.StatusOK, targets)
}

func (s *Server) handleLatencyHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	hours, err := strconv.Atoi(r.URL.Query().Get("hours"))
	if err != nil {
		hours = 24
	}
	history, err := s.svc.LatencyHistory(r.Context(), id, hours)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, history)
}

func (s *Server) handleProbeLatency(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.svc.MeasureNodeLatency(r.Context(), id); err != nil {
		writeError(w, statusForError(err), "测量失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleTraceReturnRoutes(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.svc.TraceNodeReturnRoutes(r.Context(), id); err != nil {
		writeError(w, statusForError(err), "回程检测失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
