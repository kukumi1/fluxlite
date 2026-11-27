package komari

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/probe"
	"github.com/kukumi1/fluxlite/internal/store"
)

func fixture(t *testing.T) (*Site, *store.Store, *httptest.Server) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fluxlite.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().Unix()
	err = st.ReplaceProbe(context.Background(), store.ProbeImport{
		Clients: []store.ProbeClient{
			{UUID: "u-1", Token: "token-1", Name: "HK", Info: json.RawMessage(`{"cpu_name":"EPYC","ipv4":"192.0.2.10","mem_total":1000,"version":"1.1"}`),
				Display: json.RawMessage(`{"region":"🇭🇰","price":9.9,"expired_at":"2027-03-01 00:00:00+00:00","remark":"private"}`), CreatedAt: now, UpdatedAt: now},
			{UUID: "u-2", Token: "token-2", Name: "Secret", Hidden: true, CreatedAt: now, UpdatedAt: now},
		},
		Tasks:    []store.ProbeTask{{ID: 7, Name: "成都联通", Type: "tcp", Target: "192.0.2.1:443", Interval: 60, Clients: []string{"u-1", "u-2"}}},
		Settings: map[string]string{SiteNameSetting: "Kukumi", ThemeSetting: "LuminaPlus", probe.ThemeSettingsPrefix + "LuminaPlus": `{"hiddenNodes":[]}`, PublicHostSetting: "status.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	themes := t.TempDir()
	dist := filepath.Join(themes, "LuminaPlus", "dist")
	if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(themes, "LuminaPlus", "komari-theme.json"), []byte(`{"short":"LuminaPlus"}`), 0o644)
	os.WriteFile(filepath.Join(dist, "index.html"), []byte(`<html><head><title>Komari Monitor</title></head><body></body></html>`), 0o644)
	os.WriteFile(filepath.Join(dist, "assets", "app.js"), []byte(`console.log(1)`), 0o644)
	os.WriteFile(filepath.Join(themes, "secret.txt"), []byte(`nope`), 0o644)

	hub := probe.New(st, slog.Default())
	site := New(st, hub, themes, false, slog.Default())
	srv := httptest.NewServer(site)
	t.Cleanup(srv.Close)
	return site, st, srv
}

func call(t *testing.T, srv *httptest.Server, method, params string, cookie *http.Cookie) map[string]json.RawMessage {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/rpc2", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":`+params+`}`))
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestNodesHideWhatVisitorsShouldNotSee(t *testing.T) {
	_, _, srv := fixture(t)
	var nodes map[string]map[string]any
	if err := json.Unmarshal(call(t, srv, "common:getNodes", `{}`, nil)["result"], &nodes); err != nil {
		t.Fatal(err)
	}
	hk, ok := nodes["u-1"]
	if !ok || len(nodes) != 1 {
		t.Fatalf("a hidden machine is not listed: %v", nodes)
	}
	for _, k := range []string{"ipv4", "remark", "version", "token"} {
		if _, ok := hk[k]; ok {
			t.Errorf("%s is shown to a visitor", k)
		}
	}
	if hk["region"] != "🇭🇰" || hk["cpu_name"] != "EPYC" || hk["expired_at"] != "2027-03-01T00:00:00Z" || hk["name"] != "HK" {
		t.Fatalf("%v", hk)
	}
	if e := call(t, srv, "common:getRecords", `{"type":"load","uuid":"u-2","hours":1}`, nil)["error"]; e == nil {
		t.Fatal("a hidden machine's history is refused")
	}
}

func TestUnknownMethodsSendTheThemeToItsFallback(t *testing.T) {
	_, _, srv := fixture(t)
	var e rpcError
	if err := json.Unmarshal(call(t, srv, "admin:listClients", `{}`, nil)["error"], &e); err != nil || e.Code != errNoMethod {
		t.Fatalf("%+v %v", e, err)
	}
}

func TestRecords(t *testing.T) {
	_, st, srv := fixture(t)
	ctx := context.Background()
	bucket := time.Now().Add(-10*time.Minute).Unix() / 60 * 60
	if err := st.AddProbeSamples(ctx, []store.ProbeSample{{Client: "u-1", Bucket: bucket, N: 2, CPU: 30, RAM: 800, NetIn: 400, TotalDown: 5000}}); err != nil {
		t.Fatal(err)
	}
	lo, hi := 40.0, 60.0
	if err := st.AddProbePings(ctx, []store.ProbePing{{Task: 7, Client: "u-1", Bucket: bucket, Sent: 4, Lost: 1, Sum: 150, Min: &lo, Max: &hi}}); err != nil {
		t.Fatal(err)
	}
	var load struct {
		Count   int                         `json:"count"`
		Records map[string][]map[string]any `json:"records"`
	}
	if err := json.Unmarshal(call(t, srv, "common:getRecords", `{"type":"load","uuid":"u-1","hours":1}`, nil)["result"], &load); err != nil {
		t.Fatal(err)
	}
	r := load.Records["u-1"]
	if load.Count != 1 || len(r) != 1 || r[0]["cpu"] != 15.0 || r[0]["ram"] != 400.0 || r[0]["ram_total"] != 1000.0 || r[0]["net_in"] != 200.0 {
		t.Fatalf("a bucket reads as the average of its reports: %+v", load)
	}

	var ping struct {
		Records []map[string]any `json:"records"`
		Tasks   []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(call(t, srv, "common:getRecords", `{"type":"ping","uuid":"u-1","hours":1}`, nil)["result"], &ping); err != nil {
		t.Fatal(err)
	}
	if len(ping.Records) != 1 || ping.Records[0]["value"] != 50.0 || ping.Records[0]["count"] != 4.0 || ping.Records[0]["loss"] != 25.0 {
		t.Fatalf("a bucket carries its average answer and its loss: %+v", ping.Records)
	}
	if len(ping.Tasks) != 1 || ping.Tasks[0]["loss"] != 25.0 || ping.Tasks[0]["avg"] != 50.0 || ping.Tasks[0]["target"] != nil {
		t.Fatalf("a task is summarised without its target: %+v", ping.Tasks)
	}

	var tasks []map[string]any
	if err := json.Unmarshal(call(t, srv, "public:getPublicPingTasks", `{}`, nil)["result"], &tasks); err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || len(tasks[0]["clients"].([]any)) != 1 || tasks[0]["target"] != nil {
		t.Fatalf("a hidden machine is not named in a task: %+v", tasks)
	}
}

func TestPageAndThemeFiles(t *testing.T) {
	_, _, srv := fixture(t)
	if code, body := get(t, srv.URL+"/some/theme/route"); code != 200 || !strings.Contains(body, "<title>Kukumi</title>") {
		t.Fatalf("the theme's routes get its index: %d %s", code, body)
	}
	if code, body := get(t, srv.URL+"/assets/app.js"); code != 200 || body != "console.log(1)" {
		t.Fatal(code, body)
	}
	for _, p := range []string{"/themes/LuminaPlus/../secret.txt", "/themes/LuminaPlus/%2e%2e/secret.txt", "/assets/../../secret.txt"} {
		if _, body := get(t, srv.URL+p); strings.Contains(body, "nope") {
			t.Fatalf("%s escaped the theme", p)
		}
	}
	if code, body := get(t, srv.URL+"/api/public"); code != 200 || !strings.Contains(body, `"sitename":"Kukumi"`) || !strings.Contains(body, `"theme_settings":{"hiddenNodes":[]}`) {
		t.Fatal(code, body)
	}
	if code, body := get(t, srv.URL+"/api/admin/client/list"); code != 404 || !strings.Contains(body, `"status":"error"`) {
		t.Fatal(code, body)
	}
}

func TestThemeSettingsNeedThePanelsLink(t *testing.T) {
	site, st, srv := fixture(t)
	save := func(cookie *http.Cookie) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/admin/theme/settings?theme=LuminaPlus", bytes.NewReader([]byte(`{"accent":"lime"}`)))
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if save(nil) != http.StatusUnauthorized {
		t.Fatal("a visitor cannot save the theme's settings")
	}
	link, err := site.LoginURL(context.Background())
	if err != nil || !strings.HasPrefix(link, "https://status.example.com/?probe_login=") {
		t.Fatal(link, err)
	}
	token := strings.TrimPrefix(link, "https://status.example.com/?probe_login=")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	exchange := func() *http.Cookie {
		resp, err := client.Get(srv.URL + "/?probe_login=" + token)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		for _, c := range resp.Cookies() {
			if c.Name == sessionCookie {
				return c
			}
		}
		return nil
	}
	cookie := exchange()
	if cookie == nil || !cookie.HttpOnly {
		t.Fatal("the link gives a session")
	}
	if exchange() != nil {
		t.Fatal("a link works once")
	}
	if save(cookie) != http.StatusOK {
		t.Fatal("the operator can save")
	}
	if v, _ := st.ProbeSetting(context.Background(), probe.ThemeSettingsPrefix+"LuminaPlus"); v != `{"accent":"lime"}` {
		t.Fatal(v)
	}
	var nodes map[string]any
	json.Unmarshal(call(t, srv, "common:getNodes", `{}`, cookie)["result"], &nodes)
	if len(nodes) != 2 {
		t.Fatal("the operator sees hidden machines too")
	}
}

func TestServesOnlyItsDomain(t *testing.T) {
	site, _, _ := fixture(t)
	for host, want := range map[string]bool{"status.example.com": true, "STATUS.example.com:443": true, "panel.example.com": false, "": false} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = host
		if site.Serves(r) != want {
			t.Errorf("%q: %v", host, !want)
		}
	}
}
