package service

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestQualityHosts(t *testing.T) {
	if got := qualityHost("四川移动", 4); got != "sc-cm-v4.ip.zstaticcdn.com" {
		t.Fatal(got)
	}
	if got := qualityHost("内蒙古电信", 6); got != "nm-ct-v6.ip.zstaticcdn.com" {
		t.Fatal(got)
	}
	for _, p := range SpeedProvinces {
		if provinceCodes[p] == "" {
			t.Errorf("%s has no Zstatic code", p)
		}
	}
}

func TestQualityRequests(t *testing.T) {
	var every []string
	for _, p := range SpeedProvinces {
		for _, c := range SpeedCarriers {
			every = append(every, p+c)
		}
	}
	quality := SpeedRequest{NodeIDs: []int64{1}, Points: every, Mode: "single", Family: 4, Items: []string{ItemRoute, ItemLarge, ItemUDP}}
	if err := quality.validate(); err != nil {
		t.Fatalf("every point can be checked without a speed test: %v", err)
	}
	if quality.units() != len(every) || quality.has(ItemSpeed) {
		t.Fatal(quality.units())
	}
	withSpeed := quality
	withSpeed.Items = append(withSpeed.Items, ItemSpeed)
	if withSpeed.validate() == nil {
		t.Fatal("a speed test still takes nine points at most")
	}
	for _, items := range [][]string{{"ping"}, {ItemRoute, ItemRoute}} {
		r := SpeedRequest{NodeIDs: []int64{1}, Points: []string{"四川电信"}, Mode: "single", Family: 4, Items: items}
		if r.validate() == nil {
			t.Errorf("accepted %v", items)
		}
	}
	old := SpeedRequest{NodeIDs: []int64{1}, Points: []string{"四川电信"}, Mode: "both", Family: 4}
	if !old.has(ItemSpeed) || old.quality() || old.units() != 2 {
		t.Fatal("a job saved before items were chosen was a speed test")
	}
}

func TestQualityScriptParses(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	targets := []qualityTarget{{point: "四川电信", ip: "192.0.2.1"}, {point: "西藏移动", err: "西藏移动 没有 IPv6 节点"}, {point: "四川移动", ip: "192.0.2.2"}}
	script := qualityScript("/usr/local/lib/fluxlite/nexttrace-1.7.6", targets, SpeedRequest{Items: []string{ItemRoute, ItemLarge, ItemUDP}})
	if err := exec.Command("sh", "-n", "-c", script).Run(); err != nil {
		t.Fatalf("%v\n%s", err, script)
	}
	if strings.Contains(script, "lat.1") || !strings.Contains(script, "--psize 1400 -f 30 -m 30 -q 20") || !strings.Contains(script, " -U ") {
		t.Fatalf("a point with no node is skipped; large packets and UDP are traced:\n%s", script)
	}
}

func TestQualityParsing(t *testing.T) {
	sections := splitQuality("ignored\n== lat 0\nok 40\nok 60\nfail -\nok 1104\nok 50\n== large 2\n{}\n")
	if !strings.Contains(sections["lat 0"], "ok 60") || strings.TrimSpace(sections["large 2"]) != "{}" {
		t.Fatalf("%q", sections)
	}
	ms, loss := parseConnects(sections["lat 0"])
	if *ms != 50 || *loss != 40 {
		t.Fatalf("a resent SYN is a loss: %d %d", *ms, *loss)
	}
	if ms, loss := parseConnects("none -\n"); ms != nil || loss != nil {
		t.Fatal("no probe tool is no reading")
	}

	large := `{"Hops":[[{"Address":{"IP":"192.0.2.9"},"RTT":150000000},{"Address":{"IP":"192.0.2.9"},"RTT":160000000},{"Address":null,"RTT":0},{"Address":{"IP":"198.51.100.1"},"RTT":5000000}]]}`
	ms, loss, err := parseLarge(large, "192.0.2.9")
	if err != nil || *ms != 155 || *loss != 90 {
		t.Fatalf("two of twenty answered from the destination: %v %v %v", ms, loss, err)
	}
	if _, _, err := parseLarge("operation not permitted", "192.0.2.9"); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatal(err)
	}
}

func TestRouteLineFromTraces(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	line, err := routeLine(read("nexttrace-sh-cu-icmp.json")+"\n"+read("nexttrace-sh-cu-tcp.json"), "58.246.163.108")
	if err != nil || line != "163→联通" {
		t.Fatalf("%q %v", line, err)
	}
}

func TestDualStackRequests(t *testing.T) {
	both := SpeedRequest{NodeIDs: []int64{1}, Points: []string{"四川电信", "四川联通"}, Mode: "both", Family: FamilyBoth, Items: []string{ItemRoute, ItemSpeed}}
	if err := both.validate(); err != nil {
		t.Fatal(err)
	}
	if got := both.families(); len(got) != 2 || got[0] != 4 || got[1] != 6 {
		t.Fatal(got)
	}
	if both.units() != (2+2*2)*2 {
		t.Fatalf("every step runs once per family: %d", both.units())
	}
	if (SpeedRequest{NodeIDs: []int64{1}, Points: []string{"四川电信"}, Mode: "single", Family: 5, Items: []string{ItemRoute}}).validate() == nil {
		t.Fatal("only 4, 6 or both")
	}
}
