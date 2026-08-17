package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/kukumi1/fluxlite/internal/service"
)

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	needed, err := s.auth.SetupNeeded(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot read setup state")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"setup_needed": needed})
}

type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	enrollment, err := s.auth.CreateInitialUser(r.Context(), trimmed(req.Username), req.Password)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "setup.create_user", trimmed(req.Username), "initial operator account created")
	writeJSON(w, http.StatusCreated, enrollment)
}

type setupConfirmRequest struct {
	Username string `json:"username"`
	Code     string `json:"code"`
}

func (s *Server) handleSetupConfirm(w http.ResponseWriter, r *http.Request) {
	var req setupConfirmRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.auth.ConfirmEnrollment(r.Context(), trimmed(req.Username), trimmed(req.Code)); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "setup.confirm_totp", trimmed(req.Username), "two-factor enrollment confirmed")
	writeJSON(w, http.StatusOK, map[string]bool{"enrolled": true})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Code     string `json:"code"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	token, expires, err := s.auth.Login(r.Context(), trimmed(req.Username), req.Password, trimmed(req.Code))
	if err != nil {
		s.audit(r, "auth.login_failed", trimmed(req.Username), err.Error())
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.setSessionCookie(w, token, expires)
	s.audit(r, "auth.login", trimmed(req.Username), "login succeeded")
	writeJSON(w, http.StatusOK, map[string]any{"username": trimmed(req.Username), "expires_at": expires})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if lerr := s.auth.Logout(r.Context(), cookie.Value); lerr != nil {
			s.log.Error("revoke session", "error", lerr)
		}
	}
	s.clearSessionCookie(w)
	s.audit(r, "auth.logout", "", "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	sessions, err := s.svc.Store().CountUserSessions(r.Context(), u.ID)
	if err != nil {
		s.log.Error("count sessions", "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username":      u.Username,
		"totp_enrolled": u.TOTPEnrolled,
		"created_at":    u.CreatedAt,
		"updated_at":    u.UpdatedAt,
		"sessions":      sessions,
	})
}

type changeUsernameRequest struct {
	Password string `json:"password"`
	Next     string `json:"next"`
}

func (s *Server) handleChangeUsername(w http.ResponseWriter, r *http.Request) {
	var req changeUsernameRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	u := userFrom(r.Context())
	if err := s.auth.ChangeUsername(r.Context(), u.ID, req.Password, req.Next); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "auth.username_changed", u.Username, "renamed to "+trimmed(req.Next))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleBeginTOTP(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	enrollment, err := s.auth.BeginTOTPEnrollment(r.Context(), u.ID)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "auth.totp_enrollment_started", u.Username, "")
	writeJSON(w, http.StatusOK, enrollment)
}

type totpCodeRequest struct {
	Code string `json:"code"`
}

func (s *Server) handleEnableTOTP(w http.ResponseWriter, r *http.Request) {
	var req totpCodeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	u := userFrom(r.Context())
	if err := s.auth.EnableTOTP(r.Context(), u.ID, req.Code); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "auth.totp_enabled", u.Username, "two-factor turned on")
	writeJSON(w, http.StatusOK, map[string]bool{"enrolled": true})
}

type disableTOTPRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

func (s *Server) handleDisableTOTP(w http.ResponseWriter, r *http.Request) {
	var req disableTOTPRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	u := userFrom(r.Context())
	if err := s.auth.DisableTOTP(r.Context(), u.ID, req.Password, req.Code); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "auth.totp_disabled", u.Username, "two-factor turned off")
	writeJSON(w, http.StatusOK, map[string]bool{"enrolled": false})
}

func (s *Server) handleInstallRealm(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	version, err := s.svc.InstallRealm(r.Context(), id)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "node.install_realm", strconv.FormatInt(id, 10), "realm "+version)
	writeJSON(w, http.StatusOK, map[string]string{"realm_version": version})
}

// handleRevokeSessions logs every other browser out, which is what an operator
// reaches for when they suspect a session is loose somewhere.
func (s *Server) handleRevokeSessions(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	if err := s.svc.Store().DeleteUserSessionsExcept(r.Context(), u.ID, sessionToken(r)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "auth.sessions_revoked", u.Username, "other sessions revoked")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// sessionToken returns the caller's own session token, empty when absent.
func sessionToken(r *http.Request) string {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		return cookie.Value
	}
	return ""
}

type changePasswordRequest struct {
	Current string `json:"current"`
	Next    string `json:"next"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	u := userFrom(r.Context())
	if err := s.auth.ChangePassword(r.Context(), u.ID, req.Current, req.Next, sessionToken(r)); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "auth.password_changed", u.Username, "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.svc.Store().ListNodes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nodes)
}

func (s *Server) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	var in service.NodeInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	node, err := s.svc.CreateNode(r.Context(), in)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "node.create", node.Name, node.Addr())
	writeJSON(w, http.StatusCreated, node)
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	node, err := s.svc.Store().NodeByID(r.Context(), id)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, node)
}

func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in service.NodeInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	node, err := s.svc.UpdateNode(r.Context(), id, in)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "node.update", node.Name, node.Addr())
	writeJSON(w, http.StatusOK, node)
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.svc.DeleteNode(r.Context(), id); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "node.delete", strconv.FormatInt(id, 10), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleProbeNode(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	result, err := s.svc.ProbeNode(r.Context(), id)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "node.probe", strconv.FormatInt(id, 10), result.Facts.OSID+"/"+string(result.Facts.InitSystem))
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleListRoutes(w http.ResponseWriter, r *http.Request) {
	routes, err := s.svc.Store().ListRoutes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, routes)
}

func (s *Server) handleCreateRoute(w http.ResponseWriter, r *http.Request) {
	var in service.RouteInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	route, err := s.svc.CreateRoute(r.Context(), in)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "route.create", route.Name, route.Target)
	writeJSON(w, http.StatusCreated, route)
}

func (s *Server) handleGetRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	route, err := s.svc.Store().RouteByID(r.Context(), id)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, route)
}

func (s *Server) handleUpdateRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in service.RouteInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	route, err := s.svc.UpdateRoute(r.Context(), id, in)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "route.update", route.Name, route.Target)
	writeJSON(w, http.StatusOK, route)
}

func (s *Server) handleDeleteRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	leftovers, err := s.svc.DeleteRoute(r.Context(), id)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "route.delete", strconv.FormatInt(id, 10), leftoverSummary(leftovers))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "leftovers": leftovers})
}

func leftoverSummary(leftovers []service.RouteLeftover) string {
	if len(leftovers) == 0 {
		return "removed from every hop"
	}
	names := make([]string, 0, len(leftovers))
	for _, l := range leftovers {
		names = append(names, l.NodeName)
	}
	return "relay left running on " + strings.Join(names, ", ")
}

func (s *Server) handleApplyRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	result, err := s.svc.ApplyRoute(r.Context(), id)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "route.apply", result.RouteName, applySummary(result.Failed()))
	status := http.StatusOK
	if result.Failed() {
		status = http.StatusMultiStatus
	}
	writeJSON(w, status, result)
}

func applySummary(failed bool) string {
	if failed {
		return "completed with errors"
	}
	return "applied successfully"
}

func (s *Server) handleVerifyRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	report, err := s.svc.VerifyRoute(r.Context(), id)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "route.verify", report.RouteName, verifySummary(report.Proven))
	writeJSON(w, http.StatusOK, report)
}

func verifySummary(proven bool) string {
	if proven {
		return "payload delivery proven by packet capture"
	}
	return "payload delivery not proven"
}

func (s *Server) handleStopRoute(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.svc.StopRoute(r.Context(), id); err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "route.stop", strconv.FormatInt(id, 10), "")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	statuses, err := s.svc.RouteStatuses(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, statuses)
}

// handleTraffic reports cumulative totals keyed by route id.
//
// Routes absent from the response have never been sampled. That is not the
// same as having carried nothing, so the client must render them as unknown.
func (s *Server) handleTraffic(w http.ResponseWriter, r *http.Request) {
	traffic, err := s.svc.RouteTraffic(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, traffic)
}

// handleQuotas reports period usage for every capped route.
func (s *Server) handleQuotas(w http.ResponseWriter, r *http.Request) {
	states, err := s.svc.QuotaStates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, states)
}

func (s *Server) handleRouteTraffic(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	daily, err := s.svc.DailyTraffic(r.Context(), id, days)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, daily)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := s.svc.Store().ListAudit(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}
