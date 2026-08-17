// Package api exposes the HTTP interface: a JSON API under /api and the
// embedded single-page frontend everywhere else.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/kukumi1/fluxlite/internal/auth"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/store"
)

const sessionCookie = "fluxlite_session"

type contextKey string

const userContextKey contextKey = "user"

// Server wires the API handlers together.
type Server struct {
	auth    *auth.Service
	svc     *service.Service
	log     *slog.Logger
	static  http.Handler
	secure  bool
	limiter *rateLimiter
}

// Config configures the HTTP server.
type Config struct {
	Auth    *auth.Service
	Service *service.Service
	Logger  *slog.Logger
	Static  http.Handler
	// SecureCookies marks session cookies Secure. It is on whenever the panel
	// is reached over HTTPS, which is the supported deployment.
	SecureCookies bool
}

func NewServer(cfg Config) *Server {
	return &Server{
		auth:    cfg.Auth,
		svc:     cfg.Service,
		log:     cfg.Logger,
		static:  cfg.Static,
		secure:  cfg.SecureCookies,
		limiter: newRateLimiter(10, time.Minute),
	}
}

// Handler builds the HTTP routes.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)

	r.Route("/api", func(r chi.Router) {
		// chi answers these with an empty body by default, which leaves an API
		// client staring at a bare status code.
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "no such endpoint: "+r.Method+" "+r.URL.Path)
		})
		r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusMethodNotAllowed, r.Method+" is not allowed on "+r.URL.Path)
		})

		r.Get("/setup/status", s.handleSetupStatus)
		r.With(s.rateLimit).Post("/setup", s.handleSetup)
		r.With(s.rateLimit).Post("/setup/confirm", s.handleSetupConfirm)
		r.With(s.rateLimit).Post("/login", s.handleLogin)

		// The installer authenticates with its one-shot enrollment token
		// rather than a session, since it runs on a bare machine.
		r.With(s.rateLimit).Get("/enroll/key", s.handleEnrollKey)
		r.With(s.rateLimit).Get("/enroll/realm", s.handleEnrollRealm)
		r.With(s.rateLimit).Post("/enroll/report", s.handleEnrollReport)

		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)

			r.Post("/logout", s.handleLogout)
			r.Get("/me", s.handleMe)
			r.Post("/password", s.handleChangePassword)
			r.Post("/account/username", s.handleChangeUsername)
			r.Post("/account/totp/begin", s.handleBeginTOTP)
			r.Post("/account/totp/enable", s.handleEnableTOTP)
			r.Post("/account/totp/disable", s.handleDisableTOTP)
			r.Post("/account/sessions/revoke", s.handleRevokeSessions)

			r.Post("/enroll/ticket", s.handleEnrollTicket)

			r.Get("/nodes", s.handleListNodes)
			r.Post("/nodes", s.handleCreateNode)
			r.Get("/nodes/{id}", s.handleGetNode)
			r.Put("/nodes/{id}", s.handleUpdateNode)
			r.Delete("/nodes/{id}", s.handleDeleteNode)
			r.Post("/nodes/{id}/probe", s.handleProbeNode)
			r.Post("/nodes/{id}/realm", s.handleInstallRealm)

			r.Get("/routes", s.handleListRoutes)
			r.Post("/routes", s.handleCreateRoute)
			r.Get("/routes/{id}", s.handleGetRoute)
			r.Put("/routes/{id}", s.handleUpdateRoute)
			r.Delete("/routes/{id}", s.handleDeleteRoute)
			r.Post("/routes/{id}/apply", s.handleApplyRoute)
			r.Post("/routes/{id}/verify", s.handleVerifyRoute)
			r.Post("/routes/{id}/stop", s.handleStopRoute)

			r.Get("/routes/{id}/traffic", s.handleRouteTraffic)

			r.Get("/status", s.handleStatus)
			r.Get("/traffic", s.handleTraffic)
			r.Get("/quotas", s.handleQuotas)
			r.Get("/audit", s.handleAudit)
		})
	})

	r.Get("/enroll.sh", s.handleEnrollScript)
	r.Get("/uninstall.sh", s.handleUninstallScript)

	if s.static != nil {
		r.NotFound(s.static.ServeHTTP)
	}
	return r
}

// securityHeaders sets defensive headers on every response. The CSP is strict
// because the panel renders no third-party content.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// requireAuth rejects requests without a valid session.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		user, err := s.auth.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "session expired")
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func userFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(userContextKey).(*store.User)
	return u
}

// rateLimit throttles unauthenticated endpoints per client address.
func (s *Server) rateLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.allow(clientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "too many attempts, slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateLimiter is a fixed-window counter keyed by client address.
type rateLimiter struct {
	limit  int
	window time.Duration

	mu      sync.Mutex
	entries map[string]*window
}

type window struct {
	count int
	start time.Time
}

func newRateLimiter(limit int, w time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: w, entries: make(map[string]*window)}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	e, ok := l.entries[key]
	if !ok || now.Sub(e.start) > l.window {
		l.entries[key] = &window{count: 1, start: now}
		l.sweep(now)
		return true
	}
	if e.count >= l.limit {
		return false
	}
	e.count++
	return true
}

// sweep drops expired entries so the map cannot grow without bound under a
// spray of distinct source addresses.
func (l *rateLimiter) sweep(now time.Time) {
	if len(l.entries) < 1024 {
		return
	}
	for k, e := range l.entries {
		if now.Sub(e.start) > l.window {
			delete(l.entries, k)
		}
	}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteStrictMode,
	})
}

// audit records an operator action, logging rather than failing the request
// when the write itself errors.
func (s *Server) audit(r *http.Request, action, target, detail string) {
	actor := "anonymous"
	if u := userFrom(r.Context()); u != nil {
		actor = u.Username
	}
	entry := &model.AuditLog{
		Actor:  actor,
		Action: action,
		Target: target,
		Detail: detail,
		IP:     clientIP(r),
	}
	if err := s.svc.Store().AppendAudit(r.Context(), entry); err != nil {
		s.log.Error("append audit log", "action", action, "error", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// The status line is already sent, so the only useful action left is
		// to stop writing.
		return
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// statusForError maps domain errors onto HTTP status codes so handlers do not
// each re-derive the mapping.
func statusForError(err error) int {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, store.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, auth.ErrInvalidCredentials), errors.Is(err, auth.ErrNoSession):
		return http.StatusUnauthorized
	case errors.Is(err, auth.ErrAccountLocked):
		return http.StatusTooManyRequests
	case errors.Is(err, auth.ErrSetupClosed):
		return http.StatusForbidden
	default:
		return http.StatusBadRequest
	}
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid request body: " + err.Error())
	}
	return nil
}

func trimmed(s string) string { return strings.TrimSpace(s) }
