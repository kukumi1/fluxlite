package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/kukumi1/fluxlite/internal/service"
)

func (s *Server) handleGetSingBoxClientParameters(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	result, err := s.svc.SingBoxClientParameters(r.Context(), id)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleSetSingBoxClientParameters(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req service.SingBoxClientParametersInput
	if err := decodeClientParametersRequest(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if err := s.svc.SetSingBoxClientParameters(r.Context(), id, req); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "singbox.client_parameters", strconv.FormatInt(id, 10), "client-only preferences updated")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func decodeClientParametersRequest(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(dst) != nil {
		return errors.New("请求格式错误")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("请求格式错误")
	}
	return nil
}

func (s *Server) handleResetSingBoxClientParameters(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.svc.ResetSingBoxClientParameters(r.Context(), id); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "singbox.client_parameters_reset", strconv.FormatInt(id, 10), "client-only preferences reset")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
