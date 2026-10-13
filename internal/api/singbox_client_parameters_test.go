package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/kukumi1/fluxlite/internal/singbox"
)

func TestClientParametersHTTPImportExportResetAndAuth(t *testing.T) {
	h, id, token := exportHTTPFixture(t, "private-qa-profile-secret")
	base := "/api/singbox/users/" + strconv.FormatInt(id, 10)
	request := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if authenticated {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		if w := request(method, base+"/client-parameters", "{}", false); w.Code != http.StatusUnauthorized {
			t.Fatal("client preferences endpoint missing auth guard")
		}
	}
	get := request(http.MethodGet, base+"/client-parameters", "", true)
	if get.Code != http.StatusOK || get.Header().Get("Cache-Control") != "no-store" || strings.Contains(get.Body.String(), "private-qa") || !strings.Contains(get.Body.String(), `"parameters":null`) {
		t.Fatal("safe initial profile metadata missing")
	}
	original := "anytls://private-qa-profile-secret@203.0.113.11:21200?sni=developer.apple.com&insecure=1&fp=chrome#Ignored"
	body, _ := json.Marshal(map[string]string{"original_link": original})
	post := request(http.MethodPost, base+"/client-parameters", string(body), true)
	if post.Code != http.StatusOK || post.Header().Get("Cache-Control") != "no-store" || strings.Contains(post.Body.String(), "private-qa") {
		t.Fatal("profile import failed or exposes secret")
	}
	get = request(http.MethodGet, base+"/client-parameters", "", true)
	var metadata struct {
		Parameters *singbox.ClientParameters `json:"parameters"`
		Effective  singbox.ClientParameters  `json:"effective_parameters"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	want := singbox.ClientParameters{SNI: "developer.apple.com", Insecure: true, Fingerprint: "chrome"}
	if metadata.Parameters == nil || *metadata.Parameters != want || metadata.Effective != want || strings.Contains(get.Body.String(), "private-qa") {
		t.Fatal("metadata not matching saved client options")
	}
	export := request(http.MethodGet, base+"/export", "", true)
	var exported struct {
		Link   string          `json:"link"`
		QR     string          `json:"qr_data_url"`
		Config json.RawMessage `json:"config"`
	}
	if export.Code != http.StatusOK || json.Unmarshal(export.Body.Bytes(), &exported) != nil || !strings.Contains(exported.Link, "sni=developer.apple.com") || !strings.Contains(exported.Link, "fp=chrome") || !strings.Contains(exported.Link, "insecure=1") || !strings.HasPrefix(exported.QR, "data:image/png;base64,") || !strings.Contains(string(exported.Config), `"server_name":"developer.apple.com"`) || !strings.Contains(string(exported.Config), `"fingerprint":"chrome"`) {
		t.Fatal("link/QR/client JSON do not include restored options")
	}
	for _, invalid := range []string{
		`{}`, `{"parameters":null}`, `{"original_link":"private-qa-profile-secret"}`, `{"parameters":{"sni":"example.test","insecure":false,"fingerprint":"chrome"},"original_link":"private-qa-profile-secret"}`,
		`{"private-qa-profile-secret":"unknown"}`, `{"parameters":{"sni":"https://private-qa-profile-secret","insecure":false,"fingerprint":""}}`,
	} {
		w := request(http.MethodPost, base+"/client-parameters", invalid, true)
		if w.Code == http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "private-qa-profile-secret") {
			t.Fatal("invalid profile request accepted or echoed secrets")
		}
	}
	audit := request(http.MethodGet, "/api/audit?limit=100", "", true)
	if audit.Code != http.StatusOK || strings.Contains(audit.Body.String(), original) || strings.Contains(audit.Body.String(), "private-qa-profile-secret") {
		t.Fatal("audit exposes imported credentials")
	}
	reset := request(http.MethodDelete, base+"/client-parameters", "", true)
	if reset.Code != http.StatusOK || reset.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("reset failed")
	}
	get = request(http.MethodGet, base+"/client-parameters", "", true)
	if !strings.Contains(get.Body.String(), `"parameters":null`) || strings.Contains(get.Body.String(), "developer.apple.com") {
		t.Fatal("reset retained saved profile")
	}
}
