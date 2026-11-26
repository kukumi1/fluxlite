package service

import (
	"context"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
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

func TestLatencyIPv6Targets(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestLookup(t)

	// Saved before IPv6 existed: presets get their v6 name back and follow
	// their IPv4 switch; custom points stay IPv4-only.
	saved := `[{"name":"广东电信","host":"gd-ct-v4.ip.zstaticcdn.com","port":80,"enabled":true},` +
		`{"name":"自建","host":"203.0.113.9","port":443,"enabled":true}]`
	if err := st.SetSetting(ctx, latencyTargetsKey, saved); err != nil {
		t.Fatal(err)
	}
	targets, err := svc.LatencyTargets(ctx)
	if err != nil || targets[0].HostV6 != "gd-ct-v6.ip.zstaticcdn.com" || !targets[0].EnabledV6 ||
		targets[1].HostV6 != "" || targets[1].EnabledV6 {
		t.Fatalf("%+v %v", targets, err)
	}

	noHost := []LatencyTarget{{Name: "x", Host: "203.0.113.9", Port: 80, EnabledV6: true}}
	if err := svc.SetLatencyTargets(ctx, noHost); err == nil {
		t.Fatal("switched IPv6 on for a point with no IPv6 address")
	}
	// The two families are chosen independently, and a choice to leave IPv6
	// off survives a reload.
	v6Only := []LatencyTarget{
		{Name: "a", Host: "203.0.113.9", HostV6: "2001:db8::9", Port: 80, EnabledV6: true},
		{Name: "b", Host: "203.0.113.10", HostV6: "2001:db8::10", Port: 80, Enabled: true},
	}
	if err := svc.SetLatencyTargets(ctx, v6Only); err != nil {
		t.Fatal(err)
	}
	if targets, _ := svc.LatencyTargets(ctx); targets[1].EnabledV6 {
		t.Fatalf("IPv6 switched back on by itself: %+v", targets)
	}
	if resolved, err := svc.resolveTargets(ctx); err != nil || len(resolved) != 2 ||
		resolved[0].Name != "a" || resolved[0].family != 6 || resolved[1].Name != "b" || resolved[1].family != 4 {
		t.Fatalf("families not chosen per point: %+v %v", resolved, err)
	}

	bad := []LatencyTarget{{Name: "x", Host: "203.0.113.9", HostV6: "203.0.113.9", Port: 80, Enabled: true}}
	if err := svc.SetLatencyTargets(ctx, bad); err == nil {
		t.Fatal("accepted an IPv4 address as the IPv6 host")
	}
	good := []LatencyTarget{{Name: "x", Host: "203.0.113.9", HostV6: "2001:db8::9", Port: 80, Enabled: true, EnabledV6: true}}
	if err := svc.SetLatencyTargets(ctx, good); err != nil {
		t.Fatal(err)
	}
	resolved, err := svc.resolveTargets(ctx)
	if err != nil || len(resolved) != 2 || resolved[0].family != 4 || resolved[1].family != 6 || resolved[1].ip != "2001:db8::9" {
		t.Fatalf("%+v %v", resolved, err)
	}

	if got := forNode(resolved, &model.Node{}); len(got) != 1 || got[0].family != 4 {
		t.Fatalf("machine without IPv6 still probes IPv6: %+v", got)
	}
	if got := forNode(resolved, &model.Node{IPv6Address: "2001:db8::1"}); len(got) != 2 {
		t.Fatalf("machine with IPv6 lost its IPv6 points: %+v", got)
	}

	if _, err := resolveIP(ctx, "2001:db8::9", 4); err == nil {
		t.Fatal("an IPv6 literal resolved for IPv4")
	}
	samples := parseLatency("0 ok 40\n1 ok 70\n", resolved, 1, 9)
	if len(samples) != 2 || samples[0].Family != 4 || samples[1].Family != 6 {
		t.Fatalf("%+v", samples)
	}
}
