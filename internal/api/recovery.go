package api

import (
	"errors"
	"net/http"

	"github.com/kukumi1/fluxlite/internal/auth"
)

func (s *Server) recoveryNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleRecoveryStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.auth.RecoveryStatus(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		s.recoveryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

type configureRecoveryRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

func (s *Server) handleConfigureRecovery(w http.ResponseWriter, r *http.Request) {
	var req configureRecoveryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	u := userFrom(r.Context())
	code, err := s.auth.ConfigureRecovery(r.Context(), u.ID, req.Password, req.Code, req.Question, req.Answer)
	if err != nil {
		s.recoveryError(w, err)
		return
	}
	s.audit(r, "auth.recovery_configured", u.Username, "")
	writeJSON(w, http.StatusOK, map[string]string{"recovery_code": code})
}

func (s *Server) handleDisableRecovery(w http.ResponseWriter, r *http.Request) {
	var req disableTOTPRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	u := userFrom(r.Context())
	if err := s.auth.DisableRecovery(r.Context(), u.ID, req.Password, req.Code); err != nil {
		s.recoveryError(w, err)
		return
	}
	s.audit(r, "auth.recovery_disabled", u.Username, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type recoveryQuestionRequest struct {
	Username     string `json:"username"`
	RecoveryCode string `json:"recovery_code"`
}

func (s *Server) handleRecoveryQuestion(w http.ResponseWriter, r *http.Request) {
	var req recoveryQuestionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	question, err := s.auth.GetRecoveryQuestion(r.Context(), req.Username, req.RecoveryCode)
	if err != nil {
		s.recoveryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, question)
}

type recoveryResetRequest struct {
	Username     string `json:"username"`
	RecoveryCode string `json:"recovery_code"`
	Answer       string `json:"answer"`
	Code         string `json:"code"`
	Password     string `json:"password"`
}

func (s *Server) handleRecoveryReset(w http.ResponseWriter, r *http.Request) {
	var req recoveryResetRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if err := s.auth.ResetRecoveredPassword(r.Context(), req.Username, req.RecoveryCode, req.Answer, req.Code, req.Password); err != nil {
		s.recoveryError(w, err)
		return
	}
	s.clearSessionCookie(w)
	s.audit(r, "auth.password_recovered", trimmed(req.Username), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) recoveryError(w http.ResponseWriter, err error) {
	if errors.Is(err, auth.ErrInvalidRecovery) {
		writeError(w, http.StatusUnauthorized, auth.ErrInvalidRecovery.Error())
		return
	}
	// Validation messages contain no submitted values; unexpected database or
	// crypto errors must not be returned to unauthenticated recovery callers.
	if errors.Is(err, auth.ErrRecoveryInput) {
		writeError(w, http.StatusBadRequest, err.Error())
	} else {
		s.log.Error("recovery operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "暂时无法处理，请稍后重试")
	}
}
