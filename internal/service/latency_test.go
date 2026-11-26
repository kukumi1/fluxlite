package service

import (
	"testing"

	"github.com/kukumi1/fluxlite/internal/store"
)

func TestParseLatency(t *testing.T) {
	targets := []resolvedTarget{
		{LatencyTarget: LatencyTarget{Name: "广东电信"}},
		{LatencyTarget: LatencyTarget{Name: "上海联通"}},
		{LatencyTarget: LatencyTarget{Name: "北京移动"}},
		{LatencyTarget: LatencyTarget{Name: "无工具"}},
	}
	out := "0 ok 40\n0 ok 60\n0 fail -\n0 ok 50\n" + // three of four, average 50
		"1 fail -\n1 fail -\n1 fail -\n1 fail -\n" + // all lost
		"2 ok -\n2 ok -\n2 ok -\n2 ok -\n" + // connected, nothing to time with
		"3 none -\n3 none -\n" + // no probe tool at all: no sample
		"9 ok 1\ngarbage\n"
	got := parseLatency(out, targets, 7, 100)
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	want := []struct {
		name string
		avg  int
		loss int
	}{{"广东电信", 50, 25}, {"上海联通", -1, 100}, {"北京移动", -1, 0}}
	for i, w := range want {
		g := got[i]
		avg := -1
		if g.AvgMS != nil {
			avg = *g.AvgMS
		}
		if g.Target != w.name || avg != w.avg || g.Loss != w.loss || g.NodeID != 7 || g.At != 100 {
			t.Errorf("%d: %+v avg %d", i, g, avg)
		}
	}
}

func TestReturnRouteClassification(t *testing.T) {
	trace := func(lines string) string { return classifyReturnRoute(parseTrace(lines)) }
	cases := map[string]string{
		" 1  10.0.0.1  0.4 ms\n 2  59.43.182.1  30 ms\n 3  59.43.246.9  35 ms\n 4  14.22.119.35  60 ms\n": "电信 CN2 GIA",
		" 1  59.43.182.1  30 ms\n 2  202.97.12.5  40 ms\n":                                                "电信 CN2 GT",
		" 1  202.97.12.5  40 ms\n 2  *\n":                                                                 "电信 163",
		" 1  218.105.2.1  20 ms\n":                                                                        "联通 9929",
		" 1  219.158.3.1  20 ms\n":                                                                        "联通 4837",
		" 1  223.120.2.1  20 ms\n":                                                                        "移动 CMI",
		" 1  10.0.0.1  0.4 ms\n 2  *\n 3  *\n":                                                            routeUnknown,
		"traceroute to 1.2.3.4 (1.2.3.4), 24 hops max\n 1  10.0.0.1  0.4 ms\n":                            routeUnknown,
	}
	for in, want := range cases {
		if got := trace(in); got != want {
			t.Errorf("%q: got %s, want %s", in, got, want)
		}
	}
	hops := parseTrace(" 1  223.120.2.1  20 ms\n")
	hops[0].Net = "CMIN2"
	if classifyReturnRoute(hops) != "移动 CMIN2" {
		t.Fatal("CMIN2 not named")
	}
	if hops := parseTrace(" 2  *\n"); len(hops) != 1 || hops[0].IP != "" || hops[0].TTL != 2 {
		t.Fatalf("silent hop %+v", hops)
	}
}

func TestSplitTraces(t *testing.T) {
	got := splitTraces("== 0\n 1  59.43.1.1  3 ms\n== 1\n 1  202.97.1.1  4 ms\n", 2)
	if classifyReturnRoute(parseTrace(got[0])) != "电信 CN2 GIA" || classifyReturnRoute(parseTrace(got[1])) != "电信 163" {
		t.Fatalf("%q", got)
	}
	for _, out := range splitTraces("FLUXLITE_NO_TRACEROUTE\n", 2) {
		if out != "FLUXLITE_NO_TRACEROUTE" {
			t.Fatal("missing traceroute not reported per point")
		}
	}
}

func TestLatencyVerdict(t *testing.T) {
	ms := func(v int) *int { return &v }
	cfg := AlertSettings{LossPercent: 20}
	cases := []struct {
		sample store.LatencySample
		cfg    AlertSettings
		bad    bool
	}{
		{store.LatencySample{AvgMS: ms(50), Loss: 0}, cfg, false},
		{store.LatencySample{AvgMS: ms(50), Loss: 25}, cfg, true},
		{store.LatencySample{Loss: 100}, cfg, true},
		{store.LatencySample{AvgMS: ms(400), Loss: 0}, cfg, false},
		{store.LatencySample{AvgMS: ms(400), Loss: 0}, AlertSettings{LossPercent: 20, LatencyMS: 300}, true},
		{store.LatencySample{AvgMS: ms(50), Loss: 25}, AlertSettings{LossPercent: 50}, false},
	}
	for i, c := range cases {
		if got := latencyVerdict(c.sample, c.cfg) != ""; got != c.bad {
			t.Errorf("%d: bad=%v want %v", i, got, c.bad)
		}
	}
}
