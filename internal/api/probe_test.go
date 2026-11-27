package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kukumi1/fluxlite/internal/komari"
	"github.com/kukumi1/fluxlite/internal/probe"
)

// The public domain reaches the public page alone: none of the panel's API
// answers there, and the panel's own domain is unchanged.
func TestPublicDomainServesOnlyThePage(t *testing.T) {
	s, st := newAuditTestServer(t)
	hub := probe.New(st, slog.Default())
	s.probe, s.site = hub, komari.New(st, hub, t.TempDir(), false, slog.Default())
	if err := st.SetProbeSetting(context.Background(), komari.PublicHostSetting, "status.example.com"); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for host, want := range map[string]string{"status.example.com": `"status":"error"`, "panel.example.com": `"error":"not authenticated"`} {
		r := httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d %s", host, w.Code, w.Body.String())
		}
	}
}

func TestPublicDomainCannotBeThePanels(t *testing.T) {
	s, st := newAuditTestServer(t)
	hub := probe.New(st, slog.Default())
	s.probe, s.site = hub, komari.New(st, hub, t.TempDir(), false, slog.Default())
	for body, code := range map[string]int{
		`{"public_host":"panel.example.com"}`:            http.StatusBadRequest,
		`{"public_host":"https://status.example.com/x"}`: http.StatusBadRequest,
		`{"public_host":"Status.Example.com"}`:           http.StatusOK,
	} {
		r := httptest.NewRequest(http.MethodPut, "/api/probe/site", strings.NewReader(body))
		r.Host = "panel.example.com:443"
		w := httptest.NewRecorder()
		s.handleSaveProbeSite(w, r)
		if w.Code != code {
			t.Errorf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	if v, _ := st.ProbeSetting(context.Background(), komari.PublicHostSetting); v != "status.example.com" {
		t.Fatal(v)
	}
}
