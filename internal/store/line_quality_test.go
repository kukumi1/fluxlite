package store

import (
	"context"
	"testing"
)

func TestLatencySamplesLatestHistoryAndPrune(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	route := seedRoute(t, st)
	node := route.Hops[0].NodeID
	ms := func(v int) *int { return &v }

	if err := st.AddLatencySamples(ctx, []LatencySample{
		{NodeID: node, Target: "广东电信", At: 1000, AvgMS: ms(40), Loss: 0},
		{NodeID: node, Target: "广东电信", At: 1100, AvgMS: ms(60), Loss: 25},
		{NodeID: node, Target: "广东电信", At: 1300, AvgMS: nil, Loss: 100},
		{NodeID: node, Target: "上海联通", At: 1100, AvgMS: ms(80), Loss: 0},
		{NodeID: node, Target: "广东电信", Family: 6, At: 1100, AvgMS: ms(90), Loss: 0},
	}); err != nil {
		t.Fatal(err)
	}

	latest, err := st.LatestLatency(ctx, 1050)
	if err != nil || len(latest) != 3 {
		t.Fatalf("%+v %v", latest, err)
	}
	for _, l := range latest {
		if l.Target == "广东电信" && l.Family == 6 && (l.At != 1100 || *l.AvgMS != 90) {
			t.Fatalf("IPv6 sample mixed with IPv4: %+v", l)
		}
		if l.Target == "广东电信" && l.Family == 4 && (l.At != 1300 || l.AvgMS != nil || l.Loss != 100) {
			t.Fatalf("latest picked the wrong sample: %+v", l)
		}
	}

	history, err := st.LatencyHistory(ctx, node, 4, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	// 1000 and 1100 share the bucket at 1000; 1300 has one of its own.
	var first *LatencySample
	for i := range history {
		if history[i].Target == "广东电信" && history[i].At == 1000 {
			first = &history[i]
		}
	}
	if first == nil || first.AvgMS == nil || *first.AvgMS != 50 || first.Loss != 13 {
		t.Fatalf("bucket average wrong: %+v", history)
	}

	if err := st.PruneLatency(ctx, 1200); err != nil {
		t.Fatal(err)
	}
	if v6, _ := st.LatencyHistory(ctx, node, 6, 0, 200); len(v6) != 0 {
		t.Fatalf("prune kept IPv6 samples %+v", v6)
	}
	if left, _ := st.LatencyHistory(ctx, node, 4, 0, 200); len(left) != 1 {
		t.Fatalf("prune kept %+v", left)
	}
}

func TestReturnRouteUpsertAndSettings(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	node := seedRoute(t, st).Hops[0].NodeID

	for _, line := range []string{"CN2 GIA", "163"} {
		r := ReturnRoute{NodeID: node, Target: "广东电信", Line: line, Confidence: "confirmed", Verdict: "{}", Hops: "[]", CheckedAt: 1}
		if err := st.SetReturnRoute(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	routes, err := st.ReturnRoutes(ctx)
	if err != nil || len(routes) != 1 || routes[0].Line != "163" || routes[0].Confidence != "confirmed" {
		t.Fatalf("%+v %v", routes, err)
	}

	if _, ok, err := st.Setting(ctx, "missing"); ok || err != nil {
		t.Fatal("missing setting reported present")
	}
	if err := st.SetSetting(ctx, "k", "1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "k", "2"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := st.Setting(ctx, "k"); v != "2" || !ok || err != nil {
		t.Fatalf("%q %v %v", v, ok, err)
	}
}

func TestBlockChecksReplacePerMachine(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	node := seedRoute(t, st).Hops[0].NodeID

	first := []BlockCheck{
		{Family: 4, Kind: "tcp_blocked", Label: "TCP 阻断", Targets: `[{"name":"广东电信"}]`, CheckedAt: 100},
		{Family: 6, Kind: "ok", Label: "正常", Targets: `[]`, CheckedAt: 100},
	}
	if err := st.ReplaceBlockChecks(ctx, node, first); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceBlockChecks(ctx, node, []BlockCheck{{Family: 4, Kind: "ok", Label: "正常", Targets: `[]`, CheckedAt: 200}}); err != nil {
		t.Fatal(err)
	}
	got, err := st.BlockChecks(ctx)
	if err != nil || len(got) != 1 || got[0].NodeID != node || got[0].Kind != "ok" || got[0].CheckedAt != 200 {
		t.Fatalf("a new run replaces the machine's old families: %+v %v", got, err)
	}
}

func TestSpeedJobsKeepRunsAndResults(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	node := seedRoute(t, st).Hops[0].NodeID
	mbps := func(v float64) *float64 { return &v }

	first, err := st.CreateSpeedJob(ctx, 100, `{"mode":"single"}`, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSpeedRun(ctx, SpeedRun{JobID: first, NodeID: node, Egress: "香港", TrafficBytes: 300}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSpeedResult(ctx, SpeedResult{JobID: first, NodeID: node, Point: "上海电信", Mode: "single", RTT: "30ms", Down: mbps(80), Up: mbps(40)}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSpeedJob(ctx, first); err == nil {
		t.Fatal("deleted a running job")
	}
	if err := st.FinishSpeedJob(ctx, first, 200, "done"); err != nil {
		t.Fatal(err)
	}
	jobs, runs, results, err := st.SpeedJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].Status != "done" || runs[first][0].TrafficBytes != 300 ||
		*results[first][0].Down != 80 || results[first][0].Retrans != nil {
		t.Fatalf("%+v %+v %+v %v", jobs, runs, results, err)
	}

	for i := 0; i < 2; i++ {
		if _, err := st.CreateSpeedJob(ctx, int64(300+i), `{}`, 2); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.InterruptSpeedJobs(ctx, 400); err != nil {
		t.Fatal(err)
	}
	jobs, _, results, _ = st.SpeedJobs(ctx)
	if len(jobs) != 2 || jobs[0].Status != "interrupted" || len(results[first]) != 0 {
		t.Fatalf("only the newest two are kept, the oldest with its results: %+v", jobs)
	}
}
