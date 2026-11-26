package service

import (
	"os/exec"
	"testing"
)

const taierOutput = "\n\x1b[1m\x1b[36m泰尔测速\x1b[0m\n\x1b[2m  出口 203.0.113.9  香港 PCCW\x1b[0m\n\n[\n  {\n    \"region\": \"上海电信\",\n    \"family\": \"IPv4\",\n    \"rtt\": \"31ms\",\n    \"single_up_f\": 40.2,\n    \"single_down_f\": 80.5,\n    \"multi_up_f\": 0,\n    \"multi_down_f\": 0,\n    \"retrans\": \"1.2%\",\n    \"retrans_f\": 1.2,\n    \"retried\": 1\n  }\n]\n@@rc 0\n"

func TestParseSpeedRun(t *testing.T) {
	egress, p := parseSpeed(taierOutput, "single")
	if egress != "203.0.113.9 香港 PCCW" {
		t.Fatalf("egress %q", egress)
	}
	if p.Error != "" || p.RTT != "31ms" || *p.Down != 80.5 || *p.Up != 40.2 || p.Retrans == nil || *p.Retrans != 1.2 || p.Retried != 1 {
		t.Fatalf("%+v", p)
	}
	_, multi := parseSpeed(taierOutput, "multi")
	if *multi.Down != 0 || *multi.Up != 0 {
		t.Fatalf("multi reads the multi-thread columns: %+v", multi)
	}
}

func TestParseSpeedFailures(t *testing.T) {
	_, p := parseSpeed("@@rc 1\n@@err [X] 不支持的测速点: 火星电信\n", "single")
	if p.Error != "[X] 不支持的测速点: 火星电信" || p.Down != nil {
		t.Fatalf("%+v", p)
	}
	_, p = parseSpeed("泰尔测速\n@@rc 124\n", "single")
	if p.Error != "测速超时" {
		t.Fatalf("%+v", p)
	}
	_, p = parseSpeed("[{\"region\":\"x\",\"retrans_f\":-1,\"single_down_f\":5}]\n@@rc 0\n", "single")
	if p.Retrans != nil || *p.Down != 5 {
		t.Fatalf("unreadable retransmits stay unknown: %+v", p)
	}
}

func TestSpeedRequestValidation(t *testing.T) {
	ok := SpeedRequest{NodeIDs: []int64{1}, Points: []string{"上海电信", "内蒙古移动"}, Mode: "both", Family: 4}
	if err := ok.validate(); err != nil {
		t.Fatal(err)
	}
	bad := []SpeedRequest{
		{NodeIDs: []int64{1}, Points: []string{"上海电信'; rm -rf / #"}, Mode: "single", Family: 4},
		{NodeIDs: []int64{1}, Points: []string{"上海"}, Mode: "single", Family: 4},
		{NodeIDs: []int64{1}, Points: []string{"台湾电信"}, Mode: "single", Family: 4},
		{NodeIDs: []int64{1}, Points: []string{"上海电信", "上海电信"}, Mode: "single", Family: 4},
		{NodeIDs: []int64{1}, Points: []string{"上海电信"}, Mode: "fast", Family: 4},
		{NodeIDs: []int64{1}, Points: []string{"上海电信"}, Mode: "single", Family: 5},
		{Points: []string{"上海电信"}, Mode: "single", Family: 4},
	}
	for _, r := range bad {
		if r.validate() == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	if len(ok.modes()) != 2 {
		t.Fatal("both runs single and multi apart")
	}
}

func TestSpeedScriptParses(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	script := speedScript("/usr/local/lib/fluxlite/taierspeedtest-1.0.7", "上海电信", "single", 4)
	if err := exec.Command("sh", "-n", "-c", script).Run(); err != nil {
		t.Fatalf("%v\n%s", err, script)
	}
}

func TestMaskEgress(t *testing.T) {
	cases := map[string]string{
		"203.0.113.9 香港 PCCW":              "203.0.*.* 香港 PCCW",
		"2404:3881:1:2::5 美因河畔法兰克福 Cogent": "2404:3881:**** 美因河畔法兰克福 Cogent",
		"":        "",
		"香港 PCCW": "香港 PCCW",
	}
	for in, want := range cases {
		if got := MaskEgress(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestParseTraffic(t *testing.T) {
	got, err := parseTraffic("1834567 99001\n")
	if err != nil || got.rx != 1834567 || got.tx != 99001 {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := parseTraffic("garbage"); err == nil {
		t.Fatal("accepted a broken reading")
	}
}
