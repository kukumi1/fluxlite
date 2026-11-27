// Package komari serves the probe's public status page: a Komari theme,
// unchanged, on the probe's own domain, with the part of Komari's API the
// theme reads. Only what the theme needs is served there; the panel's own
// API is not reachable on that domain.
package komari

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/kukumi1/fluxlite/internal/probe"
	"github.com/kukumi1/fluxlite/internal/store"
)

// Settings the public page uses, kept with the probe's.
const (
	PublicHostSetting = "public_host"
	// PanelURLSetting is where the panel was last opened from; the theme's
	// links into Komari's back office go there instead.
	PanelURLSetting    = "panel_url"
	SiteNameSetting    = "sitename"
	DescriptionSetting = "description"
	CustomHeadSetting  = "custom_head"
	CustomBodySetting  = "custom_body"
	ThemeSetting       = "theme"
)

// DefaultTheme is the one theme the page serves when none is set.
const DefaultTheme = "LuminaPlus"

const (
	sessionCookie = "fluxlite_probe_session"
	sessionFor    = 12 * time.Hour
	loginFor      = 2 * time.Minute
	settingsTTL   = 5 * time.Second
)

// Site is the public page and its API.
type Site struct {
	store    *store.Store
	hub      *probe.Hub
	log      *slog.Logger
	themes   string
	secure   bool
	handler  http.Handler
	mu       sync.Mutex
	logins   map[string]time.Time // one-time tokens from the panel
	sessions map[string]time.Time // cookies they were exchanged for
	cached   map[string]string
	cachedAt time.Time
	earliest map[int64]int64
	earlyAt  time.Time
}

// New makes the public page; themes is the directory holding each theme's
// own directory.
func New(st *store.Store, hub *probe.Hub, themes string, secure bool, log *slog.Logger) *Site {
	s := &Site{store: st, hub: hub, log: log, themes: themes, secure: secure, logins: map[string]time.Time{}, sessions: map[string]time.Time{}}
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Route("/api", func(r chi.Router) {
		hub.Routes(r)
		r.Get("/public", s.handlePublic)
		r.Get("/me", s.handleMe)
		r.Post("/rpc2", s.handleRPCPost)
		r.Get("/rpc2", s.handleRPCSocket)
		r.Post("/admin/theme/settings", s.handleSaveThemeSettings)
		r.Get("/admin/client/list", s.handleAdminClients)
		r.Get("/admin/ping", s.handleAdminPing)
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			writeWrapped(w, http.StatusNotFound, nil, "Not found")
		})
	})
	r.Get("/favicon.ico", s.handleFavicon)
	r.Get("/sw.js", handleRetiredWorker)
	r.Get("/admin", s.handleBackOffice)
	r.Get("/admin/*", s.handleBackOffice)
	r.Get("/themes/{id}/*", s.handleThemeFile)
	r.NotFound(s.handlePage)
	s.handler = r
	return s
}

func (s *Site) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// settings reads the page's settings, a few seconds stale at most: the
// theme asks for them on every load.
func (s *Site) settings(ctx context.Context) map[string]string {
	s.mu.Lock()
	if s.cached != nil && time.Since(s.cachedAt) < settingsTTL {
		out := s.cached
		s.mu.Unlock()
		return out
	}
	s.mu.Unlock()
	out := map[string]string{}
	for _, key := range []string{PublicHostSetting, SiteNameSetting, DescriptionSetting, CustomHeadSetting, CustomBodySetting, ThemeSetting, PanelURLSetting} {
		v, err := s.store.ProbeSetting(ctx, key)
		if err != nil {
			s.log.Error("read probe setting", "key", key, "error", err)
		}
		out[key] = v
	}
	if out[ThemeSetting] == "" {
		out[ThemeSetting] = DefaultTheme
	}
	s.mu.Lock()
	s.cached, s.cachedAt = out, time.Now()
	s.mu.Unlock()
	return out
}

// historyStart is where each resolution's history begins, re-read a minute
// at a time: it only moves as old buckets are pruned.
func (s *Site) historyStart(ctx context.Context) (map[int64]int64, error) {
	s.mu.Lock()
	if s.earliest != nil && time.Since(s.earlyAt) < time.Minute {
		out := s.earliest
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()
	out, err := s.store.ProbeEarliest(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.earliest, s.earlyAt = out, time.Now()
	s.mu.Unlock()
	return out, nil
}

// Forget drops the cached settings after they are changed.
func (s *Site) Forget() {
	s.mu.Lock()
	s.cached = nil
	s.mu.Unlock()
}

// Serves reports whether a request is for the public page's domain.
func (s *Site) Serves(r *http.Request) bool {
	host := s.settings(r.Context())[PublicHostSetting]
	if host == "" {
		return false
	}
	h := r.Host
	if i := strings.LastIndexByte(h, ':'); i > 0 && !strings.HasSuffix(h, "]") {
		h = h[:i]
	}
	return strings.EqualFold(h, host)
}

// themeSettingsView is the theme's own query for its settings page.
const themeSettingsView = "theme-manage"

// LoginURL makes a link that opens the theme's settings page with the
// panel's operator signed in for a while.
func (s *Site) LoginURL(ctx context.Context) (string, error) {
	host := s.settings(ctx)[PublicHostSetting]
	if host == "" {
		return "", errors.New("还没有设置公开页域名")
	}
	token := rand.Text()
	s.mu.Lock()
	s.logins[token] = time.Now().Add(loginFor)
	s.mu.Unlock()
	return "https://" + host + "/?view=" + themeSettingsView + "&probe_login=" + token, nil
}

// take consumes a valid token from a set; expired ones are dropped on the way.
func take(set map[string]time.Time, token string, now time.Time, consume bool) bool {
	for k, exp := range set {
		if now.After(exp) {
			delete(set, k)
		}
	}
	_, ok := set[token]
	if ok && consume {
		delete(set, token)
	}
	return ok
}

func (s *Site) loggedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return take(s.sessions, c.Value, time.Now(), false)
}

// exchange turns a login link into a session cookie.
func (s *Site) exchange(w http.ResponseWriter, r *http.Request, token string) {
	s.mu.Lock()
	ok := take(s.logins, token, time.Now(), true)
	session := ""
	if ok {
		session = rand.Text()
		s.sessions[session] = time.Now().Add(sessionFor)
	}
	s.mu.Unlock()
	if ok {
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: session, Path: "/", MaxAge: int(sessionFor.Seconds()),
			HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode})
	}
	// Only the settings page is a destination, so the link cannot be made
	// to send its holder anywhere else.
	dest := "/"
	if r.URL.Query().Get("view") == themeSettingsView {
		dest = "/?view=" + themeSettingsView
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// writeWrapped answers the way Komari's REST routes do.
func writeWrapped(w http.ResponseWriter, code int, data any, message string) {
	status := "success"
	if code >= 400 {
		status = "error"
	}
	writeJSON(w, code, map[string]any{"status": status, "message": message, "data": data})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Site) handlePublic(w http.ResponseWriter, r *http.Request) {
	set := s.settings(r.Context())
	themeSettings := json.RawMessage("{}")
	if raw, err := s.store.ProbeSetting(r.Context(), probe.ThemeSettingsPrefix+set[ThemeSetting]); err == nil && json.Valid([]byte(raw)) && raw != "" {
		themeSettings = json.RawMessage(raw)
	}
	writeWrapped(w, http.StatusOK, map[string]any{
		"sitename": set[SiteNameSetting], "description": set[DescriptionSetting], "theme": set[ThemeSetting],
		"custom_head": set[CustomHeadSetting], "custom_body": set[CustomBodySetting], "theme_settings": themeSettings,
		"private_site": false, "disable_password_login": true, "oauth_enable": false, "oauth_provider": "",
		"record_enabled": true, "record_preserve_time": 168, "ping_record_preserve_time": 168, "metric_retention_days": 7,
		"cors_origin_check_enabled": false, "visitor_audit_enabled": false,
	}, "")
}

func (s *Site) handleMe(w http.ResponseWriter, r *http.Request) {
	if s.loggedIn(r) {
		writeJSON(w, http.StatusOK, map[string]any{"logged_in": true, "username": "admin", "uuid": "", "2fa_enabled": false, "sso_id": "", "sso_type": ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logged_in": false, "username": "Guest"})
}

func (s *Site) handleSaveThemeSettings(w http.ResponseWriter, r *http.Request) {
	if !s.loggedIn(r) {
		writeWrapped(w, http.StatusUnauthorized, nil, "Unauthorized.")
		return
	}
	theme := r.URL.Query().Get("theme")
	if theme == "" || theme != s.settings(r.Context())[ThemeSetting] {
		writeWrapped(w, http.StatusBadRequest, nil, "Unknown theme")
		return
	}
	var body json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil || len(body) == 0 || body[0] != '{' {
		writeWrapped(w, http.StatusBadRequest, nil, "Invalid settings")
		return
	}
	if err := s.store.SetProbeSetting(r.Context(), probe.ThemeSettingsPrefix+theme, string(body)); err != nil {
		s.log.Error("save theme settings", "error", err)
		writeWrapped(w, http.StatusInternalServerError, nil, "Failed to save")
		return
	}
	writeWrapped(w, http.StatusOK, nil, "")
}

// handleAdminClients lists every machine, hidden ones and addresses too, for
// the theme's settings page to bind latency checks to.
func (s *Site) handleAdminClients(w http.ResponseWriter, r *http.Request) {
	if !s.loggedIn(r) {
		writeWrapped(w, http.StatusUnauthorized, nil, "Unauthorized.")
		return
	}
	clients, err := s.store.ProbeClients(r.Context())
	if err != nil {
		s.log.Error("list probe clients", "error", err)
		writeWrapped(w, http.StatusInternalServerError, nil, "Failed to get client info")
		return
	}
	out := make([]map[string]any, 0, len(clients))
	for _, c := range clients {
		out = append(out, node(c, true))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAdminPing lists every latency check with its target, for the theme's
// settings page.
func (s *Site) handleAdminPing(w http.ResponseWriter, r *http.Request) {
	if !s.loggedIn(r) {
		writeWrapped(w, http.StatusUnauthorized, nil, "Unauthorized.")
		return
	}
	tasks, err := s.store.ProbeTasks(r.Context())
	if err != nil {
		s.log.Error("list probe tasks", "error", err)
		writeWrapped(w, http.StatusInternalServerError, nil, "Failed to get ping tasks")
		return
	}
	writeWrapped(w, http.StatusOK, tasks, "")
}

// retiredWorker replaces the service worker Komari's pages installed. Left
// in place it answers page loads from Komari's cache, and the theme reads
// its failing updates as a broken back office; this one clears the cache
// and removes itself.
const retiredWorker = `self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    for (const key of await caches.keys()) await caches.delete(key);
    await self.registration.unregister();
    for (const client of await self.clients.matchAll({ type: "window" })) client.navigate(client.url);
  })());
});
`

func handleRetiredWorker(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, retiredWorker)
}

// handleBackOffice sends the theme's links into Komari's back office, which
// the panel replaces, to the panel's probe page.
func (s *Site) handleBackOffice(w http.ResponseWriter, r *http.Request) {
	panel := s.settings(r.Context())[PanelURLSetting]
	if panel == "" {
		http.Error(w, "后台在 fluxlite 面板的「探针」页", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, panel+"/?tab=probe", http.StatusFound)
}

// themeDir is the active theme's directory.
func (s *Site) themeDir(ctx context.Context) string {
	return filepath.Join(s.themes, filepath.Base(s.settings(ctx)[ThemeSetting]))
}

// file opens a file under a directory, refusing anything that is not a
// regular file inside it.
func file(dir, name string) (string, bool) {
	clean := path.Clean("/" + name)
	p := filepath.Join(dir, filepath.FromSlash(clean))
	if rel, err := filepath.Rel(dir, p); err != nil || strings.HasPrefix(rel, "..") {
		return "", false
	}
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return p, true
}

func (s *Site) handleThemeFile(w http.ResponseWriter, r *http.Request) {
	id := filepath.Base(chi.URLParam(r, "id"))
	if id != s.settings(r.Context())[ThemeSetting] {
		http.NotFound(w, r)
		return
	}
	p, ok := file(filepath.Join(s.themes, id), chi.URLParam(r, "*"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, p)
}

// handleFavicon serves the site's own icon, brought over from Komari, or
// else the theme's.
func (s *Site) handleFavicon(w http.ResponseWriter, r *http.Request) {
	for _, dir := range []string{s.themes, filepath.Join(s.themeDir(r.Context()), "dist")} {
		if p, ok := file(dir, "favicon.ico"); ok {
			http.ServeFile(w, r, p)
			return
		}
	}
	http.NotFound(w, r)
}

// handlePage serves the theme's files, and its index for every other path
// so the theme's own routes work.
func (s *Site) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	if token := r.URL.Query().Get("probe_login"); token != "" {
		s.exchange(w, r, token)
		return
	}
	dist := filepath.Join(s.themeDir(r.Context()), "dist")
	if r.URL.Path != "/" {
		if p, ok := file(dist, r.URL.Path); ok && filepath.Base(p) != "index.html" {
			http.ServeFile(w, r, p)
			return
		}
	}
	index, err := os.ReadFile(filepath.Join(dist, "index.html"))
	if err != nil {
		http.Error(w, "公开页的主题还没有安装", http.StatusServiceUnavailable)
		return
	}
	set := s.settings(r.Context())
	page := string(index)
	if set[SiteNameSetting] != "" {
		page = strings.Replace(page, "<title>Komari Monitor</title>", "<title>"+htmlEscape(set[SiteNameSetting])+"</title>", 1)
	}
	if set[DescriptionSetting] != "" {
		page = strings.Replace(page, "A simple server monitor tool.", htmlEscape(set[DescriptionSetting]), 1)
	}
	page = strings.Replace(page, "</head>", set[CustomHeadSetting]+"</head>", 1)
	page = strings.Replace(page, "</body>", set[CustomBodySetting]+"</body>", 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(page))
}

var htmlReplacer = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")

func htmlEscape(s string) string { return htmlReplacer.Replace(s) }
