package service

import (
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

func nodeSubject(id string, state observation) alertSubject {
	return alertSubject{key: "node:" + id, kind: alertNode, name: "M" + id, state: state}
}

func routeSubjectFor(id string, state observation) alertSubject {
	return alertSubject{key: "route:" + id, kind: alertRoute, name: "R" + id, state: state, hold: routeAlertHold, reasons: []string{"why"}}
}

func TestAlertTrackerFirstPassIsSilent(t *testing.T) {
	var tr alertTracker
	now := time.Now()
	if ev := tr.observe(now, []alertSubject{nodeSubject("1", obsFailing), routeSubjectFor("1", obsFailing)}); len(ev) != 0 {
		t.Fatalf("restart replayed known state: %+v", ev)
	}
	// Still failing: nothing new to say.
	if ev := tr.observe(now.Add(time.Minute), []alertSubject{nodeSubject("1", obsFailing), routeSubjectFor("1", obsFailing)}); len(ev) != 0 {
		t.Fatalf("%+v", ev)
	}
	// Recovery of something already broken at startup is still reported.
	ev := tr.observe(now.Add(2*time.Minute), []alertSubject{nodeSubject("1", obsOK), routeSubjectFor("1", obsFailing)})
	if len(ev) != 1 || ev[0].failing || ev[0].subject.key != "node:1" {
		t.Fatalf("%+v", ev)
	}
}

func TestAlertTrackerNodeReportsAtOnceAndMeasuresOutage(t *testing.T) {
	var tr alertTracker
	t0 := time.Now()
	tr.observe(t0, []alertSubject{nodeSubject("1", obsOK)})

	ev := tr.observe(t0.Add(10*time.Second), []alertSubject{nodeSubject("1", obsFailing)})
	if len(ev) != 1 || !ev[0].failing {
		t.Fatalf("offline not reported at once: %+v", ev)
	}
	ev = tr.observe(t0.Add(10*time.Minute+10*time.Second), []alertSubject{nodeSubject("1", obsOK)})
	if len(ev) != 1 || ev[0].failing || ev[0].lasted != 10*time.Minute {
		t.Fatalf("%+v", ev)
	}
}

func TestAlertTrackerRouteNeedsToStayFailed(t *testing.T) {
	var tr alertTracker
	t0 := time.Now()
	tr.observe(t0, []alertSubject{routeSubjectFor("1", obsOK)})

	// One failed sample followed by a good one is a blip.
	if ev := tr.observe(t0.Add(10*time.Second), []alertSubject{routeSubjectFor("1", obsFailing)}); len(ev) != 0 {
		t.Fatal("reported a single failed sample")
	}
	if ev := tr.observe(t0.Add(20*time.Second), []alertSubject{routeSubjectFor("1", obsOK)}); len(ev) != 0 {
		t.Fatal("reported recovery from a blip")
	}

	start := t0.Add(30 * time.Second)
	for i, want := range []int{0, 0, 0, 1} {
		ev := tr.observe(start.Add(time.Duration(i)*10*time.Second), []alertSubject{routeSubjectFor("1", obsFailing)})
		if len(ev) != want {
			t.Fatalf("sample %d: %d events, want %d", i, len(ev), want)
		}
	}
	// Unknown while a machine on it is offline holds the failure as reported.
	if ev := tr.observe(start.Add(time.Minute), []alertSubject{routeSubjectFor("1", obsUnknown)}); len(ev) != 0 {
		t.Fatal("unknown changed the reported state")
	}
	tr.observe(start.Add(2*time.Minute), []alertSubject{routeSubjectFor("1", obsOK)})
	ev := tr.observe(start.Add(2*time.Minute+routeAlertHold), []alertSubject{routeSubjectFor("1", obsOK)})
	if len(ev) != 1 || ev[0].failing || ev[0].lasted != 2*time.Minute {
		t.Fatalf("%+v", ev)
	}
}

func TestAlertTrackerForgetsRemovedSubjects(t *testing.T) {
	var tr alertTracker
	t0 := time.Now()
	tr.observe(t0, []alertSubject{routeSubjectFor("1", obsOK)})
	for i := 1; i <= 4; i++ {
		tr.observe(t0.Add(time.Duration(i)*10*time.Second), []alertSubject{routeSubjectFor("1", obsFailing)})
	}
	// Stopped by hand: gone from the list, and no "recovered" for it.
	if ev := tr.observe(t0.Add(time.Minute), nil); len(ev) != 0 {
		t.Fatalf("%+v", ev)
	}
	// Started again later, healthy: nothing to report either.
	if ev := tr.observe(t0.Add(2*time.Minute), []alertSubject{routeSubjectFor("1", obsOK)}); len(ev) != 0 {
		t.Fatalf("%+v", ev)
	}
}

func TestRouteSubject(t *testing.T) {
	now := time.Now()
	recent := now.Add(-10 * time.Second)
	old := now.Add(-time.Hour)
	yes, no := true, false
	nodes := map[int64]*model.Node{
		1: {ID: 1, Name: "HK", Status: model.StatusOnline},
		2: {ID: 2, Name: "TW", Status: model.StatusOnline},
		3: {ID: 3, Name: "JP", Status: model.StatusOffline},
	}
	hop := func(node int64, running, reachable *bool, at time.Time) model.RouteHop {
		return model.RouteHop{NodeID: node, Running: running, CheckedAt: &at, Reachable: reachable, ReachAt: &at}
	}
	route := func(hops ...model.RouteHop) *model.Route {
		return &model.Route{ID: 7, Name: "R", Target: "203.0.113.9:443", Hops: hops}
	}

	cases := []struct {
		name    string
		route   *model.Route
		state   observation
		reasons []string
	}{
		{"healthy", route(hop(1, &yes, &yes, recent), hop(2, &yes, &yes, recent)), obsOK, nil},
		{"relay down", route(hop(1, &yes, &yes, recent), hop(2, &no, &yes, recent)), obsFailing, []string{"TW 上的转发进程没在运行"}},
		{"landing dead", route(hop(1, &yes, &yes, recent), hop(2, &yes, &no, recent)), obsFailing, []string{"TW 连不上落地 203.0.113.9:443"}},
		{"next hop dead", route(hop(1, &yes, &no, recent), hop(2, &yes, &yes, recent)), obsFailing, []string{"HK 连不上下一跳 TW"}},
		{"next hop relay down said once", route(hop(1, &yes, &no, recent), hop(2, &no, &yes, recent)), obsFailing, []string{"TW 上的转发进程没在运行"}},
		{"machine offline", route(hop(1, &yes, &yes, recent), hop(3, &no, &no, recent)), obsUnknown, nil},
		{"stale sample", route(hop(1, &yes, &yes, recent), hop(2, &no, &no, old)), obsUnknown, nil},
		{"never sampled", route(model.RouteHop{NodeID: 1}), obsUnknown, nil},
		{"unmeasured link is not dead", route(hop(1, &yes, nil, recent)), obsOK, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := routeSubject(c.route, nodes, now)
			if got.state != c.state || strings.Join(got.reasons, "|") != strings.Join(c.reasons, "|") {
				t.Fatalf("got %v %q, want %v %q", got.state, got.reasons, c.state, c.reasons)
			}
		})
	}
}

func TestAlertMessage(t *testing.T) {
	seen := time.Date(2026, 11, 25, 9, 31, 0, 0, time.UTC)
	msg := alertMessage([]alertEvent{
		{subject: alertSubject{kind: alertNode, name: "DataWave-SG", seen: &seen, routes: []string{"A", "B"}}, failing: true},
		{subject: alertSubject{kind: alertRoute, name: "CDT-DATA-TW", reasons: []string{"DATA-TW 连不上落地 x:1"}}, failing: true},
		{subject: alertSubject{kind: alertRoute, name: "Lam-DATA-TW"}, lasted: 95 * time.Minute},
	})
	want := "🔴 机器掉线：DataWave-SG\n最后在线：11-25 17:31\n受影响链路：A、B\n\n" +
		"🔴 链路失效：CDT-DATA-TW\n· DATA-TW 连不上落地 x:1\n\n" +
		"🟢 链路已恢复：Lam-DATA-TW（中断 1 小时 35 分钟）"
	if msg != want {
		t.Fatalf("got\n%s\nwant\n%s", msg, want)
	}
	if alertMessage(nil) != "" {
		t.Fatal("empty digest produced a message")
	}
}
