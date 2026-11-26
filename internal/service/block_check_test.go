package service

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/store"
)

func blockTargetsFor(families ...int) []resolvedTarget {
	var out []resolvedTarget
	for i, f := range families {
		out = append(out, resolvedTarget{LatencyTarget: LatencyTarget{Name: []string{"广东电信", "上海联通", "北京移动"}[i%3], Port: 80}, family: f, ip: "192.0.2.1"})
	}
	return out
}

func TestBlockVerdicts(t *testing.T) {
	tcp := func(i int, ok bool) string {
		r := "fail -"
		if ok {
			r = "ok 40"
		}
		return strings.Repeat(string(rune('0'+i))+" tcp "+r+"\n", blockProbes)
	}
	cases := []struct {
		name string
		out  string
		want BlockKind
	}{
		{"all fine", "0 icmp 0\n1 icmp 0\n" + tcp(0, true) + tcp(1, true), BlockNone},
		{"points ignore ping", "0 icmp 100\n1 icmp 100\n" + tcp(0, true) + tcp(1, true), BlockNone},
		{"one carrier down", "0 icmp 0\n1 icmp 0\n" + tcp(0, true) + tcp(1, false), BlockPartial},
		{"tcp blocked", "0 icmp 25\n1 icmp 100\n" + tcp(0, false) + tcp(1, false), BlockTCP},
		{"address blocked", "0 icmp 100\n1 icmp 100\n" + tcp(0, false) + tcp(1, false), BlockIP},
		{"no ping on machine", "0 icmp none\n1 icmp none\n" + tcp(0, false) + tcp(1, false), BlockTCPUnknown},
		{"no tcp tool", "0 icmp 0\n0 tcp none -\n1 tcp none -\n", BlockNoTool},
	}
	for _, c := range cases {
		got := judgeBlock(parseBlock(c.out, blockTargetsFor(4, 4)))
		if len(got) != 1 || got[0].Family != 4 || got[0].Kind != c.want {
			t.Errorf("%s: got %+v, want %s", c.name, got, c.want)
		}
	}
}

func TestBlockFamiliesAreJudgedApart(t *testing.T) {
	out := "0 icmp 0\n1 icmp 100\n" +
		strings.Repeat("0 tcp fail -\n", blockProbes) + strings.Repeat("1 tcp ok 90\n", blockProbes)
	got := judgeBlock(parseBlock(out, blockTargetsFor(4, 6)))
	if len(got) != 2 || got[0].Kind != BlockTCP || got[1].Kind != BlockNone {
		t.Fatalf("%+v", got)
	}
	v4only := judgeBlock(parseBlock(out, blockTargetsFor(4)))
	if len(v4only) != 1 || v4only[0].Family != 4 {
		t.Fatalf("a machine without IPv6 points gets no IPv6 verdict: %+v", v4only)
	}
}

func TestBlockParsesFractionalLoss(t *testing.T) {
	got := parseBlock("0 icmp 33.3333\n9 icmp 0\ngarbage\n", blockTargetsFor(4))
	if got[0].ICMPLoss == nil || *got[0].ICMPLoss != 33 {
		t.Fatalf("%+v", got[0])
	}
}

func TestBlockScriptRuns(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	script := blockScript(blockTargetsFor(4, 6))
	if err := exec.Command("sh", "-n", "-c", script).Run(); err != nil {
		t.Fatalf("script does not parse: %v\n%s", err, script)
	}
}

func TestOnlyNewBlocksAreReported(t *testing.T) {
	cases := []struct {
		previous string
		now      BlockKind
		want     bool
	}{
		{"", BlockTCP, true},
		{"ok", BlockIP, true},
		{"tcp_blocked", BlockIP, true},
		{"tcp_blocked", BlockTCP, false},
		{"ip_blocked", BlockNone, false},
		{"ok", BlockPartial, false},
		{"", BlockNoTool, false},
	}
	for _, c := range cases {
		if got := newlyBlocked(c.previous, c.now); got != c.want {
			t.Errorf("%q → %s: got %v", c.previous, c.now, got)
		}
	}
}

func TestBlockCheckIsDueEverySixHours(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	if !blockCheckDue(nil, now) {
		t.Fatal("never checked is due")
	}
	recent := []store.BlockCheck{{CheckedAt: now.Add(-5 * time.Hour).Unix()}, {CheckedAt: now.Add(-7 * time.Hour).Unix()}}
	if blockCheckDue(recent, now) {
		t.Fatal("the newest check decides")
	}
	if !blockCheckDue([]store.BlockCheck{{CheckedAt: now.Add(-6 * time.Hour).Unix()}}, now) {
		t.Fatal("six hours old is due")
	}
}

func TestBlockNoticesFollowTheirOwnSwitch(t *testing.T) {
	cfg := defaultAlertSettings()
	if !cfg.Events.Blocked {
		t.Fatal("on by default")
	}
	if err := json.Unmarshal([]byte(`{"events":{"route_change":false}}`), &cfg); err != nil || !cfg.Events.Blocked {
		t.Fatalf("settings saved before the switch existed keep it on: %+v %v", cfg.Events, err)
	}
}
