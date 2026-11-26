package service

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/kukumi1/fluxlite/internal/routeclass"
	"github.com/kukumi1/fluxlite/internal/store"
)

// nextTraceSample has the shape NextTrace prints with -j: per TTL, every
// probe, with an absent Address for one that timed out and RTT in ns.
const nextTraceSample = `{"Hops":[
[{"Success":true,"Address":{"IP":"10.0.0.1","Zone":""},"Hostname":"","TTL":1,"RTT":900000,"Error":null,"Geo":null,"Lang":"","MPLS":null}],
[{"Success":false,"Address":null,"Hostname":"","TTL":2,"RTT":0,"Error":{},"Geo":null},{"Success":false,"Address":null,"TTL":2,"RTT":0}],
[{"Success":true,"Address":{"IP":"59.43.182.1","Zone":""},"TTL":3,"RTT":31250000},{"Success":true,"Address":{"IP":"59.43.182.1","Zone":""},"TTL":3,"RTT":30120000}],
[{"Success":true,"Address":{"IP":"59.43.246.9","Port":80,"Zone":""},"TTL":4,"RTT":35000000}],
[{"Success":true,"Address":{"IP":"14.22.119.35","Port":80,"Zone":""},"TTL":5,"RTT":60000000}]
],"StopReason":{"hop":5,"reason":"destination"},"TraceMapUrl":""}`

func TestParseNextTrace(t *testing.T) {
	hops, err := parseNextTrace("some banner line\n" + nextTraceSample + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) != 5 || hops[1].IP != "" || hops[2].IP != "59.43.182.1" || *hops[2].MS != 30.12 || hops[4].TTL != 5 {
		t.Fatalf("%+v", hops)
	}

	for out, want := range map[string]string{
		"":                                    "没有输出",
		"socket: operation not permitted":     "root",
		"tcp trace: dial error something odd": "路由追踪失败",
		`{"Hops":[[{"Address":null}]]}`:       "没有任何跳点回应",
	} {
		if _, err := parseNextTrace(out); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", out, err, want)
		}
	}
}

func TestTraceScriptBatches(t *testing.T) {
	var targets []resolvedReturn
	for i, p := range ReturnPresets()[:6] {
		targets = append(targets, resolvedReturn{ReturnTarget: p, ip: "1.2.3." + string(rune('0'+i))})
	}
	script := traceScript("/usr/local/lib/fluxlite/nexttrace-1.7.6", targets)
	if strings.Count(script, " -T -p 80 ") != 6 || strings.Count(script, "-q 3") != 12 || strings.Count(script, "\nwait\n") != 2 ||
		strings.Count(script, "-d disable-geoip") != 6 || strings.Count(script, " -M -j ") != 6 ||
		!strings.Contains(script, "unset NEXTTRACE_DATAPROVIDER") {
		t.Fatal(script)
	}
	got := splitTraces("== 0\na\n== 1\nb\nc\n", 2)
	if got[0] != "a\n" || got[1] != "b\nc\n\n" {
		t.Fatalf("%q", got)
	}
}

func TestReturnPresets(t *testing.T) {
	p := ReturnPresets()
	if len(p) != 12 || p[0].Name != "北京电信" || p[11].Name != "四川移动" || p[11].Host != "sc-cm-v4.ip.zstaticcdn.com" {
		t.Fatalf("%+v", p)
	}
}

func TestReturnSettingsDefaultSkipsNAT(t *testing.T) {
	ctx := context.Background()
	st, svc := newTestLookup(t)
	full := seedNode(t, st, "full")
	nat := seedNode(t, st, "nat")
	nat.PortStart, nat.PortEnd = 21005, 21009
	if err := st.UpdateNode(ctx, nat); err != nil {
		t.Fatal(err)
	}

	cfg, err := svc.ReturnSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Targets, []string{"四川电信", "四川联通", "四川移动"}) || !slices.Equal(cfg.SkipNodes, []int64{nat.ID}) {
		t.Fatalf("%+v", cfg)
	}

	if err := svc.SetReturnSettings(ctx, ReturnSettings{Targets: []string{"北京电信", "火星电信"}}); err == nil {
		t.Fatal("accepted an unknown test point")
	}
	if err := svc.SetReturnSettings(ctx, ReturnSettings{Targets: []string{"四川联通"}, SkipNodes: []int64{full.ID}}); err != nil {
		t.Fatal(err)
	}
	cfg, err = svc.ReturnSettings(ctx)
	if err != nil || !slices.Equal(cfg.Targets, []string{"四川联通"}) || !slices.Equal(cfg.SkipNodes, []int64{full.ID}) {
		t.Fatalf("%+v %v", cfg, err)
	}
}

func TestLineChanged(t *testing.T) {
	r := func(line, conf, errText string) store.ReturnRoute {
		return store.ReturnRoute{Line: line, Confidence: conf, Error: errText}
	}
	cases := []struct {
		prev, cur store.ReturnRoute
		want      bool
	}{
		{r("CN2 GIA", "confirmed", ""), r("CN2 GT", "confirmed", ""), true},
		{r("CN2 GIA", "confirmed", ""), r("CN2 GIA", "inferred", ""), false},
		{r("CN2 GIA", "confirmed", ""), r("CN2", "insufficient", ""), false},
		{r("CN2 GIA", "confirmed", ""), r("", "", "需要 root 权限才能发送追踪包"), false},
		{r("未识别", "insufficient", ""), r("163", "confirmed", ""), false},
	}
	for i, c := range cases {
		if got := lineChanged(c.prev, c.cur); got != c.want {
			t.Errorf("%d: %v, want %v", i, got, c.want)
		}
	}
}

// Real output from a Singapore machine, one ICMP and one TCP pass per test
// point, as traceScript prints them. Each pass alone missed the backbone hop
// the other caught.
func TestParseNextTraceMergesPasses(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cases := []struct {
		point, dst, blind, line, handoff string
	}{
		{"gd-ct", "14.22.119.34", "tcp", "163", ""},
		{"sh-cu", "58.246.163.108", "icmp", "163", "联通"},
	}
	for _, c := range cases {
		tcp := read("nexttrace-" + c.point + "-tcp.json")
		alone, err := parseNextTrace(read("nexttrace-" + c.point + "-" + c.blind + ".json"))
		if err != nil {
			t.Fatal(err)
		}
		if v := routeclass.Classify(annotated(alone), c.dst); v.Line == c.line && v.Handoff == c.handoff {
			t.Fatalf("%s: the %s pass alone already sees %s, the sample proves nothing", c.point, c.blind, c.line)
		}
		hops, err := parseNextTrace(read("nexttrace-"+c.point+"-icmp.json") + "\n" + tcp)
		if err != nil {
			t.Fatal(err)
		}
		v := routeclass.Classify(annotated(hops), c.dst)
		if v.Line != c.line || v.Handoff != c.handoff || !v.Reached {
			t.Errorf("%s: %+v", c.point, v)
		}
		if last := hops[len(hops)-1]; last.IP == "" {
			t.Errorf("%s: trailing silence kept", c.point)
		}
	}
}

func annotated(hops []routeclass.Hop) []routeclass.Hop {
	routeclass.Annotate(hops)
	return hops
}

// The ICMP pass carries NextTrace's IP database answers; the TCP pass has
// none, and must not erase them when the passes are merged.
func TestParseNextTraceKeepsHopDescriptions(t *testing.T) {
	icmp := `{"Hops":[[{"Address":{"IP":"202.97.116.49"},"Hostname":"","RTT":25070000,` +
		`"Geo":{"asnumber":"4134","country":"中国","prov":"上海","city":"上海","owner":"en.chinatelecom.com.cn  电信","isp":"中国电信","whois":"CHINANET-BB"}}],` +
		`[{"Address":{"IP":"154.1.1.1"},"Hostname":"ae1.example.net","RTT":30000000,"Geo":{"asnumber":"AS2914","country":"日本","owner":"","isp":"NTT"}}]]}`
	tcp := `{"Hops":[[{"Address":{"IP":"202.97.116.49"},"RTT":24000000,"Geo":{"asnumber":""}}]]}`
	hops, err := parseNextTrace(icmp + "\n" + tcp)
	if err != nil {
		t.Fatal(err)
	}
	routeclass.Annotate(hops)
	h := hops[0]
	if h.Whois != "CHINANET-BB" || h.Location != "中国 上海" || h.Owner != "en.chinatelecom.com.cn 电信" || h.ASN != 4134 || h.Net != "163" || *h.MS != 24 {
		t.Fatalf("%+v", h)
	}
	if h := hops[1]; h.ASN != 2914 || h.Owner != "NTT" || h.Hostname != "ae1.example.net" || h.Location != "日本" {
		t.Fatalf("%+v", h)
	}
}
