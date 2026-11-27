package probe

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/kukumi1/fluxlite/internal/store"
)

func fixture(t *testing.T) (*Hub, *store.Store, *httptest.Server) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fluxlite.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	err = st.ReplaceProbe(context.Background(), store.ProbeImport{
		Clients: []store.ProbeClient{{UUID: "u-1", Token: "token-1", Name: "HK"}},
		Tasks:   []store.ProbeTask{{ID: 7, Name: "成都联通", Type: "tcp", Target: "192.0.2.1:443", Interval: 60, Clients: []string{"u-1"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := New(st, slog.Default())
	if err := h.ReloadTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	r.Route("/api", h.Routes)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return h, st, srv
}

func dial(t *testing.T, srv *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

const report = `{"cpu":{"usage":12.5},"ram":{"total":1000,"used":400},"swap":{"total":0,"used":0},
	"load":{"load1":0.5,"load5":0.4,"load15":0.3},"disk":{"total":5000,"used":2500},
	"network":{"up":100,"down":300,"totalUp":7000,"totalDown":9000},"connections":{"tcp":20,"udp":4},"uptime":3600,"process":88}`

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for range 100 {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// An agent speaking v2 reports, is sent the latency checks assigned to it,
// and answers them; minutes it reported in reach every resolution.
func TestAgentV2(t *testing.T) {
	h, st, srv := fixture(t)
	conn := dial(t, srv, "/api/clients/v2/rpc?token=token-1")
	ctx := context.Background()
	send := func(s string) {
		if err := conn.Write(ctx, websocket.MessageText, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	send(`{"jsonrpc":"2.0","method":"agent.report","params":{"report":` + report + `}}`)
	waitFor(t, func() bool { return h.Snapshot(time.Now())["u-1"].Online })

	h.dispatch(ctx, time.Now())
	_, msg, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ping struct {
		Method string `json:"method"`
		Params struct {
			TaskID int64  `json:"ping_task_id"`
			Type   string `json:"ping_type"`
			Target string `json:"ping_target"`
		} `json:"params"`
	}
	if json.Unmarshal(msg, &ping) != nil || ping.Method != "agent.ping" || ping.Params.TaskID != 7 || ping.Params.Target != "192.0.2.1:443" {
		t.Fatalf("%s", msg)
	}
	h.dispatch(ctx, time.Now())
	send(`{"jsonrpc":"2.0","method":"agent.pingResult","params":{"task_id":7,"ping_type":"tcp","value":42}}`)
	send(`{"jsonrpc":"2.0","method":"agent.pingResult","params":{"task_id":7,"ping_type":"tcp","value":-1}}`)
	send(`{"jsonrpc":"2.0","id":3,"method":"agent.unknown"}`)
	if _, reply, err := conn.Read(ctx); err != nil || !strings.Contains(string(reply), "-32601") {
		t.Fatalf("a request for an unknown method is answered: %s %v", reply, err)
	}
	waitFor(t, func() bool { return len(h.Snapshot(time.Now())["u-1"].Pings[7]) == 2 })

	h.flush(ctx, time.Now().Add(2*time.Minute))
	for _, res := range store.ProbeResolutions {
		samples, err := st.ProbeSamples(ctx, "u-1", res, 0)
		if err != nil || len(samples) != 1 || samples[0].N != 1 || samples[0].CPU != 12.5 || samples[0].TotalDown != 9000 {
			t.Fatalf("resolution %d: %+v %v", res, samples, err)
		}
		pings, err := st.ProbePings(ctx, "u-1", 7, res, 0)
		if err != nil || len(pings) != 1 || pings[0].Sent != 2 || pings[0].Lost != 1 || pings[0].Sum != 42 || *pings[0].Min != 42 {
			t.Fatalf("a lost probe is counted, not averaged: %+v %v", pings, err)
		}
	}
	conn.Close(websocket.StatusNormalClosure, "")
	waitFor(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.conns["u-1"] == nil
	})
}

func TestAgentV1(t *testing.T) {
	h, _, srv := fixture(t)
	conn := dial(t, srv, "/api/clients/report?token=token-1")
	ctx := context.Background()
	if err := conn.Write(ctx, websocket.MessageText, []byte(report)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return h.Snapshot(time.Now())["u-1"].Online })
	h.dispatch(ctx, time.Now())
	_, msg, err := conn.Read(ctx)
	if err != nil || !strings.Contains(string(msg), `"message":"ping"`) || strings.Contains(string(msg), "jsonrpc") {
		t.Fatalf("a v1 agent is sent v1 messages: %s %v", msg, err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"ping_result","task_id":7,"ping_type":"tcp","value":30}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(h.Snapshot(time.Now())["u-1"].Pings[7]) == 1 })

	bad := report
	bad = strings.Replace(bad, `"usage":12.5`, `"usage":250`, 1)
	if err := conn.Write(ctx, websocket.MessageText, []byte(bad)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := h.Snapshot(time.Now())["u-1"].Latest.Report.CPU.Usage; got != 12.5 {
		t.Fatalf("an impossible reading is dropped: %v", got)
	}
}

func TestAgentAuthAndBasicInfo(t *testing.T) {
	_, st, srv := fixture(t)
	var body bytes.Buffer
	gz := gzip.NewWriter(&body)
	gz.Write([]byte(`{"jsonrpc":"2.0","method":"agent.basicInfo","params":{"info":{"cpu_name":"EPYC","cpu_cores":2,"ipv4":"","ipv6":"","version":"1.1"}}}`))
	gz.Close()
	post := func(token string, b []byte, gzipped bool) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/clients/v2/rpc?token="+token, bytes.NewReader(b))
		if gzipped {
			req.Header.Set("Content-Encoding", "gzip")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if resp := post("wrong", body.Bytes(), true); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal(resp.Status)
	}
	if resp := post("token-1", body.Bytes(), true); resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	c, err := st.ProbeClientByToken(context.Background(), "token-1")
	if err != nil || !strings.Contains(string(c.Info), `"cpu_name":"EPYC"`) || !strings.Contains(string(c.Info), `"ipv4":"127.0.0.1"`) {
		t.Fatalf("with no address given, the one it came from is kept: %s %v", c.Info, err)
	}
	if _, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/api/clients/v2/rpc?token=wrong", nil); err == nil {
		t.Fatal("a socket with a wrong token was accepted")
	}
}

func TestRegister(t *testing.T) {
	_, st, srv := fixture(t)
	register := func(key string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/clients/register?name=new-box", strings.NewReader(`{"key":"`+key+`"}`))
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if resp := register("anything-long-enough"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("with no key set, nothing registers")
	}
	if err := st.SetProbeSetting(context.Background(), DiscoveryKeySetting, "discovery-key-123"); err != nil {
		t.Fatal(err)
	}
	resp := register("discovery-key-123")
	defer resp.Body.Close()
	var out struct {
		Status string `json:"status"`
		Data   struct {
			UUID  string `json:"uuid"`
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Status != "success" || out.Data.Token == "" {
		t.Fatal(out, err)
	}
	if c, err := st.ProbeClientByToken(context.Background(), out.Data.Token); err != nil || c.Name != "new-box" || c.UUID != out.Data.UUID {
		t.Fatal(c, err)
	}
}

// komariFixture builds the parts of Komari's databases the import reads.
func komariFixture(t *testing.T, now time.Time) (string, string) {
	t.Helper()
	dir := t.TempDir()
	kpath, mpath := filepath.Join(dir, "komari.db"), filepath.Join(dir, "metrics.db")
	exec := func(path string, stmts ...string) {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		for _, s := range stmts {
			if _, err := db.Exec(s); err != nil {
				t.Fatalf("%v\n%s", err, s)
			}
		}
	}
	exec(kpath,
		"CREATE TABLE `clients` (`uuid` varchar(36),`token` varchar(255) NOT NULL,`name` varchar(100),`cpu_name` varchar(100),`virtualization` varchar(50),`arch` varchar(50),`cpu_cores` integer,`os` varchar(100),`kernel_version` varchar(100),`gpu_name` varchar(100),`ipv4` varchar(100),`ipv6` varchar(100),`region` varchar(100),`remark` longtext,`public_remark` longtext,`mem_total` bigint,`swap_total` bigint,`disk_total` bigint,`version` varchar(100),`weight` integer,`price` real,`billing_cycle` integer,`auto_renewal` numeric DEFAULT false,`currency` varchar(20) DEFAULT \"$\",`expired_at` timestamp,`group` varchar(100),`tags` text,`hidden` numeric DEFAULT false,`traffic_limit` bigint,`traffic_limit_type` varchar(10) DEFAULT \"max\",`created_at` datetime,`updated_at` datetime, `cpu_physical_cores` integer,PRIMARY KEY (`uuid`))",
		"CREATE TABLE `ping_tasks` (`id` integer PRIMARY KEY AUTOINCREMENT,`name` varchar(255) NOT NULL,`clients` longtext,`type` varchar(12) NOT NULL DEFAULT \"icmp\",`target` varchar(255) NOT NULL,`interval` integer NOT NULL DEFAULT 60, `weight` integer NOT NULL DEFAULT 0, `all_clients` numeric NOT NULL DEFAULT false)",
		"CREATE TABLE `configs` (`key` text,`value` text,PRIMARY KEY (`key`))",
		"CREATE TABLE `theme_configurations` (`short` text NOT NULL,`data` longtext,PRIMARY KEY (`short`))",
		"CREATE TABLE `offline_notifications` (`client` varchar(36) NOT NULL,`enable` boolean DEFAULT false,`grace_period` integer NOT NULL DEFAULT 180,`last_notified` datetime)",
		`INSERT INTO clients (uuid, token, name, cpu_name, region, price, auto_renewal, hidden, weight, "group", created_at, updated_at) VALUES
			('u-1', 'token-1', 'HK', 'EPYC', '🇭🇰', 9.9, true, false, 2, 'Asia', '2026-08-20 16:49:00+00:00', '2026-11-27 08:00:00+00:00'),
			('u-2', 'token-2', 'Hidden', NULL, NULL, NULL, false, true, 1, NULL, NULL, NULL)`,
		`INSERT INTO ping_tasks (id, name, clients, type, target, interval, weight, all_clients) VALUES
			(7, '成都联通', '["u-1"]', 'tcp', '192.0.2.1:443', 60, 1, false), (8, '全部', NULL, 'icmp', '192.0.2.2', 60, 0, true)`,
		`INSERT INTO configs VALUES ('sitename', '"Kukumi"'), ('theme', '"LuminaPlus"'), ('auto_discovery_key', '"discovery-key-123"'), ('api_key', '"secret"')`,
		`INSERT INTO theme_configurations VALUES ('LuminaPlus', '{"hiddenNodes":["u-2"]}'), ('Other', '{}')`,
		`INSERT INTO offline_notifications (client, enable) VALUES ('u-1', true), ('u-2', false)`)
	recent := now.Add(-time.Hour).Truncate(time.Hour).UnixMilli()
	old := now.Add(-8 * 24 * time.Hour).UnixMilli()
	exec(mpath,
		`CREATE TABLE metric_series (id INTEGER PRIMARY KEY, metric_name TEXT, entity_id TEXT, tags_hash TEXT, tags TEXT)`,
		`CREATE TABLE metric_resolutions (id INTEGER PRIMARY KEY, resolution_milli BIGINT)`,
		`CREATE TABLE metric_rollups (series_id BIGINT, resolution_id BIGINT, label_id BIGINT, bucket_milli BIGINT, count BIGINT, sum DOUBLE PRECISION, sum_sq DOUBLE PRECISION, min_val DOUBLE PRECISION, max_val DOUBLE PRECISION, first_val DOUBLE PRECISION, first_ts_milli BIGINT, last_val DOUBLE PRECISION, last_ts_milli BIGINT, digest BLOB, created_at_milli BIGINT)`,
		`INSERT INTO metric_resolutions VALUES (1, 60000), (2, 3600000), (3, 300000), (4, 86400000)`,
		`INSERT INTO metric_series VALUES (1, 'cpu.usage', 'u-1', '', '{}'), (2, 'memory.used', 'u-1', '', '{}'), (3, 'net.total.down', 'u-1', '', '{}'),
			(4, 'ping.latency_ms', 'u-1', '', '{"task_id":"7"}'), (5, 'ping.loss', 'u-1', '', '{"task_id":"7"}'), (6, 'cpu.usage', 'gone', '', '{}')`,
		`INSERT INTO metric_rollups (series_id, resolution_id, label_id, bucket_milli, count, sum, min_val, max_val) VALUES
			(1, 2, 0, `+itoa(recent)+`, 60, 600, 5, 20), (2, 2, 0, `+itoa(recent)+`, 30, 3000, 90, 110), (3, 2, 0, `+itoa(recent)+`, 60, 0, 1000, 5000),
			(4, 2, 0, `+itoa(recent)+`, 60, 2938, -1, 120), (5, 2, 0, `+itoa(recent)+`, 60, 2, 0, 1),
			(1, 4, 0, `+itoa(recent)+`, 1, 10, 10, 10), (1, 2, 0, `+itoa(old)+`, 60, 600, 5, 20), (6, 2, 0, `+itoa(recent)+`, 60, 600, 5, 20)`)
	return kpath, mpath
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestImportKomari(t *testing.T) {
	now := time.Now()
	kpath, mpath := komariFixture(t, now)
	in, err := ImportKomari(context.Background(), kpath, mpath, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Clients) != 2 || in.Clients[0].Token != "token-1" || !in.Clients[1].Hidden || in.Clients[0].CreatedAt == 0 {
		t.Fatalf("%+v", in.Clients)
	}
	var display map[string]any
	if json.Unmarshal(in.Clients[0].Display, &display) != nil || display["region"] != "🇭🇰" || display["auto_renewal"] != true || display["group"] != "Asia" {
		t.Fatalf("%s", in.Clients[0].Display)
	}
	if len(in.Tasks) != 2 || in.Tasks[0].ID != 7 || len(in.Tasks[1].Clients) != 2 {
		t.Fatalf("a check on every machine lists them all: %+v", in.Tasks)
	}
	if in.Settings["sitename"] != "Kukumi" || in.Settings[DiscoveryKeySetting] != "discovery-key-123" || in.Settings["api_key"] != "" ||
		in.Settings[ThemeSettingsPrefix+"LuminaPlus"] != `{"hiddenNodes":["u-2"]}` || in.Settings[ThemeSettingsPrefix+"Other"] != "" ||
		in.Settings[OfflineNotifySetting] != `["u-1"]` {
		t.Fatalf("%v", in.Settings)
	}
	hours := in.Samples[store.ProbeHour]
	if len(hours) != 1 || hours[0].N != 60 || hours[0].CPU != 600 || hours[0].RAM != 6000 || hours[0].TotalDown != 5000 {
		t.Fatalf("only the last week of known machines, averages kept: %+v", in.Samples)
	}
	pings := in.Pings[store.ProbeHour]
	if len(pings) != 1 || pings[0].Sent != 60 || pings[0].Lost != 2 || pings[0].Sum != 2940 || pings[0].Min != nil || *pings[0].Max != 120 {
		t.Fatalf("a lost probe's -1 is taken back out of the sum: %+v", pings)
	}

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fluxlite.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for range 2 {
		if err := st.ReplaceProbe(context.Background(), in); err != nil {
			t.Fatalf("an import can be run again: %v", err)
		}
	}
	if got, err := st.ProbeSamples(context.Background(), "", store.ProbeHour, 0); err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
}

func TestWatchReportsOutagesOnce(t *testing.T) {
	h, _, _ := fixture(t)
	type change struct {
		down   bool
		lasted time.Duration
	}
	var got []change
	h.SetStatusHook(func(_ context.Context, client string, down bool, _ time.Time, lasted time.Duration) {
		got = append(got, change{down, lasted})
	})
	var r Report
	start := time.Unix(1_800_000_000, 0)
	h.record("u-1", r, start)
	h.watch(context.Background(), start.Add(2*time.Minute))
	if len(got) != 0 {
		t.Fatal("an agent restarting is not an outage")
	}
	h.watch(context.Background(), start.Add(3*time.Minute))
	h.watch(context.Background(), start.Add(4*time.Minute))
	if len(got) != 1 || !got[0].down {
		t.Fatalf("an outage is reported once: %+v", got)
	}
	h.record("u-1", r, start.Add(10*time.Minute))
	h.watch(context.Background(), start.Add(10*time.Minute))
	h.watch(context.Background(), start.Add(11*time.Minute))
	if len(got) != 2 || got[1].down || got[1].lasted != 10*time.Minute {
		t.Fatalf("coming back is reported once, with how long it was away: %+v", got)
	}
}
