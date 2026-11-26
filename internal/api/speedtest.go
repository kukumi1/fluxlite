package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kukumi1/fluxlite/internal/service"
)

func (s *Server) handleSpeedOverview(w http.ResponseWriter, r *http.Request) {
	out, err := s.svc.SpeedOverview(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取测速记录失败")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleStartSpeedtest(w http.ResponseWriter, r *http.Request) {
	var req service.SpeedRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := s.svc.StartSpeedtest(r.Context(), req)
	switch {
	case errors.Is(err, service.ErrSpeedRequest):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, service.ErrSpeedBusy):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeError(w, statusForError(err), "开始测速失败："+err.Error())
		return
	}
	s.audit(r, "speedtest.run", fmt.Sprintf("%d 台机器", len(req.NodeIDs)),
		fmt.Sprintf("IPv%d %s：%s", req.Family, req.Mode, strings.Join(req.Points, "、")))
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (s *Server) handleCancelSpeedtest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.svc.CancelSpeedtest(r.Context(), id); err != nil {
		writeError(w, statusForError(err), "取消失败：测速已经结束")
		return
	}
	s.audit(r, "speedtest.cancel", fmt.Sprintf("测速 #%d", id), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteSpeedtest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.svc.DeleteSpeedtest(r.Context(), id); err != nil {
		writeError(w, statusForError(err), "删除失败：测速还在进行或已删除")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
