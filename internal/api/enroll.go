package api

import (
	"net/http"
	"strings"

	"github.com/kukumi1/fluxlite/internal/service"
)

// handleEnrollScript serves the installer. It carries no secret of its own:
// the token is passed as an argument by the operator, so the script itself is
// safe to fetch anonymously.
func (s *Server) handleEnrollScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write([]byte(enrollScript)); err != nil {
		s.log.Error("write enroll script", "error", err)
	}
}

// handleUninstallScript serves the teardown counterpart of the installer. It
// needs no token: it grants nothing, and it has to work on a machine the panel
// can no longer reach.
func (s *Server) handleUninstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write([]byte(uninstallScript)); err != nil {
		s.log.Error("write uninstall script", "error", err)
	}
}

// handleEnrollTicket creates a pending enrollment and returns the command.
func (s *Server) handleEnrollTicket(w http.ResponseWriter, r *http.Request) {
	var req service.EnrollRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ticket, err := s.svc.CreateEnrollTicket(r.Context(), externalBaseURL(r), req)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "enroll.ticket", req.Name, req.Host)
	writeJSON(w, http.StatusCreated, ticket)
}

// handleEnrollKey hands the installer the public key it should authorise.
func (s *Server) handleEnrollKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.svc.AuthorizedKeyForToken(r.Context(), r.URL.Query().Get("token"))
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write([]byte(key + "\n")); err != nil {
		s.log.Error("write enroll key", "error", err)
	}
}

// handleEnrollRealm serves the pinned realm build to an enrolling node, so a
// machine with poor GitHub connectivity never has to reach it.
func (s *Server) handleEnrollRealm(w http.ResponseWriter, r *http.Request) {
	arch := r.URL.Query().Get("arch")
	if arch != "amd64" && arch != "arm64" {
		writeError(w, http.StatusBadRequest, "arch must be amd64 or arm64")
		return
	}
	bin, err := s.svc.RealmBinaryForToken(r.Context(), r.URL.Query().Get("token"), arch)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write(bin); err != nil {
		s.log.Error("write realm binary", "error", err)
	}
}

// handleEnrollReport completes an enrollment and reports the verdict back to
// the terminal the operator is sitting at.
func (s *Server) handleEnrollReport(w http.ResponseWriter, r *http.Request) {
	var report service.EnrollReport
	if err := decodeJSON(r, &report); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	outcome, err := s.svc.CompleteEnroll(r.Context(), report)
	if err != nil {
		writeError(w, statusForError(err), err.Error())
		return
	}
	s.audit(r, "enroll.complete", outcome.Name, outcome.Detail)
	writeJSON(w, http.StatusOK, outcome)
}

// externalBaseURL reconstructs the address the operator's browser used, so the
// generated command points back at a URL the new node can actually reach.
// Behind a reverse proxy the scheme only survives in X-Forwarded-Proto.
func externalBaseURL(r *http.Request) string {
	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = strings.Split(proto, ",")[0]
	} else if r.TLS != nil {
		scheme = "https"
	}

	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = strings.Split(fwd, ",")[0]
	}
	return scheme + "://" + strings.TrimSpace(host)
}
