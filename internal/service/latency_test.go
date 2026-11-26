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
		{LatencyTarget: LatencyTarget{Name: "重传"}},
	}
	out := "0 ok 40\n0 ok 60\n0 fail -\n0 ok 50\n" + // three of four, average 50
		"1 fail -\n1 fail -\n1 fail -\n1 fail -\n" + // all lost
		"2 ok -\n2 ok -\n2 ok -\n2 ok -\n" + // connected, nothing to time with
		"3 none -\n3 none -\n" + // no probe tool at all: no sample
		"4 ok 80\n4 ok 1104\n4 ok 90\n4 ok\n" + // a resent SYN is a lost packet, not a 1.1 s path
		"9 ok 1\ngarbage\n"
	got := parseLatency(out, targets, 7, 100)
	if len(got) != 4 {
		t.Fatalf("%+v", got)
	}
	want := []struct {
		name string
		avg  int
		loss int
	}{{"广东电信", 50, 25}, {"上海联通", -1, 100}, {"北京移动", -1, 0}, {"重传", 85, 25}}
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
