package api

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/store"
)

func mbps(v float64) *float64 { return &v }

func speedJobFixture(mode string) *service.SpeedJobView {
	return &service.SpeedJobView{
		ID: 7, Status: "done", FinishedAt: 1_764_160_440,
		Request: service.SpeedRequest{NodeIDs: []int64{3}, Points: []string{"广东联通", "广东移动"}, Mode: mode, Family: 6},
		Runs: []store.SpeedRun{{NodeID: 3, Egress: "2404:3881:**** 美因河畔法兰克福 <Cogent>", TrafficBytes: 3_554_000_000,
			RxBytes: 1_836_000_000, TxBytes: 1_718_000_000, StartedAt: 1_764_160_163, FinishedAt: 1_764_160_440}},
		Results: []store.SpeedResult{
			{NodeID: 3, Point: "广东联通", Mode: "single", RTT: "155ms", Down: mbps(157), Up: mbps(178), Retrans: mbps(0.02)},
			{NodeID: 3, Point: "广东联通", Mode: "multi", RTT: "150ms", Down: mbps(514), Up: mbps(552)},
			{NodeID: 3, Point: "广东移动", Mode: "single", RTT: "25ms", Down: mbps(113), Up: mbps(0), Retrans: mbps(9.64), Error: "服务器忙"},
			{NodeID: 3, Point: "广东移动", Mode: "multi", RTT: "25ms", Down: mbps(511), Up: mbps(470)},
		},
	}
}

func TestTelegramSpeedReport(t *testing.T) {
	text := telegramSpeedReport(speedJobFixture("both"), 3, "V6VPS-HK")
	for _, want := range []string{
		"🇭🇰 <b>测速 | V6VPS-HK</b>",
		"协议：IPv6 | 运营商：联通、移动 | 测速点：广东 | 项目：单线程 + 多线程测速",
		"出口：2404:3881:**** 美因河畔法兰克福 &lt;Cogent&gt;",
		"🚀 <b>速度（Mbps）</b>",
		"<pre>IPv6      延迟  单↓   单↑  多↓  多↑\n广东联通   155  157   178  514  552\n广东移动    25  113  失败  511  470</pre>",
		"<pre>测速点       回程重传\n广东联通 v6     0.02%\n广东移动 v6     9.64%</pre>",
		"广东移动 单线程：服务器忙",
		"消耗流量：共 3.31 GB（下载 1.71 GB / 上传 1.60 GB）",
		"耗时 277 秒 | 2025-11-26 20:34 (UTC+8)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if strings.Contains(text, "泰尔") {
		t.Error("the result is called 测速")
	}
}

func TestTelegramSpeedReportSingleModeHasFewerColumns(t *testing.T) {
	job := speedJobFixture("single")
	job.Results = job.Results[:1]
	text := telegramSpeedReport(job, 3, "Lab")
	if strings.Contains(text, "多↓") || !strings.Contains(text, "单↓") || strings.HasPrefix(text, " ") {
		t.Fatalf("%s", text)
	}
}

func TestTelegramSpeedSettings(t *testing.T) {
	d := &telegramSpeedDraft{NodeName: "DMIT-JP", HasV6: false, telegramSpeedPrefs: telegramSpeedPrefs{
		Family: 4, Carriers: []string{"电信", "联通", "移动"}, Provinces: []string{"北京", "上海", "广东", "四川"}, Mode: "both",
		Items: []string{"route", "speed"}}}
	text, buttons := telegramSpeedSettings(d)
	if !strings.Contains(text, "共 12 个测速点，含测速时一次最多 9 个") {
		t.Fatalf("too many points is called out: %s", text)
	}
	flat := ""
	for _, row := range buttons {
		for _, b := range row {
			flat += b["text"].(string) + "|" + b["callback_data"].(string) + "\n"
			if len(b["callback_data"].(string)) > 64 {
				t.Errorf("callback data over 64 bytes: %v", b)
			}
		}
	}
	if strings.Contains(flat, "sp:fam:6") {
		t.Error("IPv6 is offered to a machine without it")
	}
	if !strings.Contains(flat, "✅ 单+多|sp:mode:both") || !strings.Contains(flat, "✅ 四川|sp:prov:四川") {
		t.Errorf("selected choices are ticked:\n%s", flat)
	}
	d.Provinces = toggleString(d.Provinces, "四川")
	if got := d.points(); len(got) != 9 || got[0] != "北京电信" || got[8] != "广东移动" {
		t.Fatalf("%v", got)
	}
}

func TestSplitPointsAndFlags(t *testing.T) {
	provs, isps := splitPoints([]string{"内蒙古移动", "上海电信", "上海移动"})
	if strings.Join(provs, ",") != "内蒙古,上海" || strings.Join(isps, ",") != "移动,电信" {
		t.Fatalf("%v %v", provs, isps)
	}
	if telegramFlag("DE-CN2") != "🇩🇪 " || telegramFlag("Bage-SG") != "🇸🇬 " || telegramFlag("lab") != "" {
		t.Fatal("flags")
	}
}

func TestOnlyTelegramSpeedTestsReportBack(t *testing.T) {
	s := &Server{}
	// A web-started job returns before touching the service, which is nil here.
	s.telegramSpeedDone(context.Background(), 1, service.SpeedRequest{})
}

// Every Bot API call the bot makes has to be allowed, or it fails silently:
// the speed test's buttons edit their message in place.
func TestTelegramCallsTheBotMakesAreAllowed(t *testing.T) {
	for _, method := range []string{"sendMessage", "editMessageText", "answerCallbackQuery", "getUpdates", "setMyCommands"} {
		if !slices.Contains(telegramMethods, method) {
			t.Errorf("%s is not allowed", method)
		}
	}
	for _, c := range telegramCommands {
		if c["command"] == "speed" {
			return
		}
	}
	t.Fatal("/speed is in the command menu")
}


func TestTelegramSpeedItems(t *testing.T) {
	d := &telegramSpeedDraft{NodeName: "Lab", telegramSpeedPrefs: telegramSpeedPrefs{
		Family: 4, Carriers: []string{"电信", "联通", "移动"}, Provinces: []string{"北京", "上海", "广东", "四川"}, Mode: "single",
		Items: []string{"udp", "route"}}}
	text, buttons := telegramSpeedSettings(d)
	if !strings.Contains(text, "共 12 个测速点，大约 5 分钟") || strings.Contains(text, "GB") {
		t.Fatalf("without a speed test every point is allowed and no traffic warning: %s", text)
	}
	for _, row := range buttons {
		for _, b := range row {
			if strings.HasPrefix(b["callback_data"].(string), "sp:mode:") {
				t.Fatal("threads only matter to a speed test")
			}
		}
	}
	if got := d.request().Items; strings.Join(got, ",") != "route,udp" {
		t.Fatalf("items go in report order: %v", got)
	}
	if !strings.Contains(text, "项目：回程、UDP回程") {
		t.Fatal(text)
	}
}
