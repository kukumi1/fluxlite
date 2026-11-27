package komari

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/store"
)

func TestIntervalFollowsTheHistoryKept(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	week, day := now.Add(-7*24*time.Hour), now.Add(-24*time.Hour)
	full := map[int64]int64{60: day.Unix() - 60, 300: week.Unix() - 300, 3600: week.Unix() - 3600}
	imported := map[int64]int64{60: now.Add(-12 * time.Hour).Unix(), 300: now.Add(-60 * time.Hour).Unix(), 3600: week.Unix() - 3600}
	for _, c := range []struct {
		name      string
		start     time.Time
		points    int
		earliest  map[int64]int64
		step, res int64
	}{
		{"an hour in minutes", now.Add(-time.Hour), 500, full, 60, 60},
		{"a day within five hundred points", day, 500, full, 300, 300},
		{"a week once five minutes reach back", week, 500, full, 1800, 300},
		{"a week of imported history is hourly", week, 500, imported, 3600, 3600},
		{"a day before minutes reach back", day, 2000, imported, 300, 300},
		{"a new panel's history all starts at once", week, 2000, map[int64]int64{60: now.Add(-time.Hour).Unix(), 300: now.Add(-time.Hour).Unix(), 3600: now.Add(-time.Hour).Unix()}, 600, 300},
	} {
		step, res := interval(c.start, now, now, c.points, c.earliest)
		if step != c.step || res != c.res {
			t.Errorf("%s: %d/%d, want %d/%d", c.name, step, res, c.step, c.res)
		}
	}
}

func TestQueryMetrics(t *testing.T) {
	_, st, srv := fixture(t)
	ctx := context.Background()
	base := time.Now().Add(-30*time.Minute).Unix() / 60 * 60
	samples := []store.ProbeSample{
		{Client: "u-1", Bucket: base, N: 2, CPU: 20, TotalUp: 1000},
		{Client: "u-1", Bucket: base + 60, N: 2, CPU: 60, TotalUp: 1600},
		{Client: "u-1", Bucket: base + 120, N: 2, CPU: 40, TotalUp: 100},
		{Client: "u-2", Bucket: base, N: 1, CPU: 99},
	}
	if err := st.AddProbeSamples(ctx, samples); err != nil {
		t.Fatal(err)
	}
	lo, hi := 40.0, 60.0
	if err := st.AddProbePings(ctx, []store.ProbePing{{Task: 7, Client: "u-1", Bucket: base, Sent: 4, Lost: 1, Sum: 150, Min: &lo, Max: &hi}}); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Series []metricSeries `json:"series"`
	}
	read := func(params string) {
		t.Helper()
		out.Series = nil
		res := call(t, srv, "public:queryMetrics", params, nil)
		if err := json.Unmarshal(res["result"], &out); err != nil {
			t.Fatalf("%s: %s", err, res["error"])
		}
	}
	read(`{"hours":1,"metric_keys":["cpu.usage","traffic.up"],"aggregation_by_metric":{"traffic.up":"sum"}}`)
	if len(out.Series) != 2 {
		t.Fatalf("a hidden machine has no series: %+v", out.Series)
	}
	cpu, traffic := out.Series[0], out.Series[1]
	if cpu.EntityID != "u-1" || len(cpu.Points) != 3 || *cpu.Points[0].Value != 10 || *cpu.Points[1].Value != 30 || cpu.Points[1].Count != 2 {
		t.Fatalf("each bucket is the average of its reports: %+v", cpu.Points)
	}
	if len(traffic.Points) != 2 || *traffic.Points[0].Value != 600 || *traffic.Points[1].Value != 100 {
		t.Fatalf("traffic is the rise of the running total, the whole total after a restart: %+v", traffic.Points)
	}

	read(`{"hours":1,"entity_ids":["u-1"],"metric_keys":["ping.latency_ms","ping.loss"],"tags":{"task_id":"7"}}`)
	if len(out.Series) != 2 || out.Series[0].Tags["task_id"] != "7" {
		t.Fatalf("%+v", out.Series)
	}
	latency, loss := out.Series[0].Points[0], out.Series[1].Points[0]
	if *latency.Value != (150.0-1)/4 || *loss.Value != 0.25 || latency.Count != 4 {
		t.Fatalf("latency averages a lost probe in as -1, as Komari's does: %v %v", *latency.Value, *loss.Value)
	}

	var e rpcError
	if err := json.Unmarshal(call(t, srv, "public:queryMetrics", `{}`, nil)["error"], &e); err != nil || e.Code != errBadParams {
		t.Fatal("an empty query is a bad request, which tells the theme the method exists")
	}

	var stats struct {
		Stats []map[string]any `json:"stats"`
	}
	if err := json.Unmarshal(call(t, srv, "public:getPingMetricStats", `{"hours":1,"entity_ids":["u-1"],"task_ids":[7]}`, nil)["result"], &stats); err != nil {
		t.Fatal(err)
	}
	s := stats.Stats
	if len(s) != 1 || s[0]["task_id"] != "7" || s[0]["total"] != 4.0 || s[0]["valid"] != 3.0 || s[0]["loss"] != 25.0 || s[0]["avg"] != 50.0 || s[0]["min"] != 40.0 {
		t.Fatalf("%+v", s)
	}
}
