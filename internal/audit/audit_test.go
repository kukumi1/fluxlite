package audit

import (
	"fmt"
	"testing"
	"time"
)

// 生产 DATA-TW 落地机 journalctl -o short-iso-precise 的原样输出。
const (
	journalFrom = "2026-10-22T05:56:40.230001+00:00 ECS290 sing-box[602]: INFO[397435] [3116717352 0ms] inbound/shadowsocks[ss2022-DATA-TW]: inbound connection from 191.222.209.254:60558"
	journalTo   = "2026-10-22T05:56:40.235374+00:00 ECS290 sing-box[602]: INFO[397435] [3116717352 0ms] inbound/shadowsocks[ss2022-DATA-TW]: inbound connection to www.gstatic.com:443"
	journalOut  = "2026-10-22T05:56:40.235374+00:00 ECS290 sing-box[602]: INFO[397435] [3116717352 0ms] outbound/direct[0]: outbound connection to www.gstatic.com:443"
	// sing-box 自己写文件、开了 timestamp 时的格式，带用户名。
	fileTo = "+0800 2026-10-22 14:02:11 INFO [42 3ms] inbound/vless[fluxlite-dmit]: [DMIT-JP-szhhaoo] inbound connection to claude.ai:443"
)

func TestParseJournalLines(t *testing.T) {
	from, ok := ParseLine(journalFrom)
	if !ok || from.Dir != From || from.Addr != "191.222.209.254:60558" || from.ConnID != 3116717352 {
		t.Fatalf("from 行解析错: %+v %v", from, ok)
	}
	to, ok := ParseLine(journalTo)
	if !ok || to.Dir != To || to.Addr != "www.gstatic.com:443" || to.Inbound != "ss2022-DATA-TW" || to.InboundType != "shadowsocks" {
		t.Fatalf("to 行解析错: %+v %v", to, ok)
	}
	if want := time.Date(2026, 10, 22, 5, 56, 40, 235374000, time.UTC); !to.At.Equal(want) {
		t.Errorf("journald 时间 %v，应为 %v", to.At, want)
	}
	if _, ok := ParseLine(journalOut); ok {
		t.Error("outbound 行不是客户端请求的目标，不能当成审计事件")
	}
}

func TestParseFileLineWithUserAndOffset(t *testing.T) {
	ev, ok := ParseLine(fileTo)
	if !ok || ev.User != "DMIT-JP-szhhaoo" || ev.Addr != "claude.ai:443" {
		t.Fatalf("文件行解析错: %+v %v", ev, ok)
	}
	if want := time.Date(2026, 10, 22, 6, 2, 11, 0, time.UTC); !ev.At.Equal(want) {
		t.Errorf("时区换算错: %v，应为 %v", ev.At, want)
	}
}

func TestParsePacketConnection(t *testing.T) {
	ev, ok := ParseLine("+0000 2026-10-22 06:00:00 INFO [7 0ms] inbound/hysteria2[h2]: inbound packet connection to 8.8.8.8:53")
	if !ok || ev.Dir != To || ev.Addr != "8.8.8.8:53" || !ev.Packet {
		t.Fatalf("UDP 行解析错: %+v %v", ev, ok)
	}
}

func TestCorrelatorPairsFromAndTo(t *testing.T) {
	c := NewCorrelator()
	now := time.Now()
	from, _ := ParseLine(journalFrom)
	to, _ := ParseLine(journalTo)
	if _, ok := c.Feed(from, now); ok {
		t.Fatal("只有 from 时不该出记录")
	}
	conn, ok := c.Feed(to, now)
	if !ok || conn.From != "191.222.209.254:60558" || conn.To != "www.gstatic.com:443" {
		t.Fatalf("合并错: %+v %v", conn, ok)
	}
}

// 面板每 10 秒的延迟探测只建连不发目标，只留下 from。它们必须悄悄过期，不能出记录。
func TestProbeConnectionsNeverProduceRecords(t *testing.T) {
	c := NewCorrelator()
	now := time.Now()
	from, _ := ParseLine(journalFrom)
	c.Feed(from, now)
	c.Prune(now.Add(2 * pendingTTL))
	if len(c.pending) != 0 {
		t.Error("没等到目标的 from 没被清掉，会一直占内存")
	}
}

func TestRingMergesRepeatedVisits(t *testing.T) {
	r := NewRing()
	at := time.Now()
	for i := range 4 {
		r.Add(Record{At: at.Add(time.Duration(i) * time.Second), Node: "51:22023:ss", Target: "www.gstatic.com:443"})
	}
	r.Add(Record{At: at, Node: "51:22023:ss", Target: "claude.ai:443"})
	r.Add(Record{At: at.Add(5 * time.Second), Node: "51:22023:ss", Target: "www.gstatic.com:443"})
	r.Add(Record{At: at.Add(5 * time.Second), Node: "51:10002:vless", Target: "www.gstatic.com:443"})

	got, total := r.Page(1, 100, Filter{})
	if total != 3 {
		t.Fatalf("应合并成 3 条，实际 %d 条", total)
	}
	if got[1].Target != "www.gstatic.com:443" || got[1].Count != 5 || got[1].Node != "51:22023:ss" {
		t.Errorf("同一节点的重复访问应合并计数: %+v", got[1])
	}
	if got[0].Node != "51:10002:vless" || got[0].Count != 1 {
		t.Errorf("另一个节点的同一访问不能并进来: %+v", got[0])
	}
}

func TestRingEvictsOldestAndPages(t *testing.T) {
	r := NewRing()
	at := time.Now()
	for i := range Capacity + 50 {
		r.Add(Record{At: at, Node: "n", Target: fmt.Sprintf("host%d:443", i)})
	}
	page, total := r.Page(1, 100, Filter{})
	if total != Capacity {
		t.Fatalf("应只留 %d 条，实际 %d", Capacity, total)
	}
	if page[0].Target != fmt.Sprintf("host%d:443", Capacity+49) {
		t.Errorf("第一页第一条应是最新的: %s", page[0].Target)
	}
	last, _ := r.Page(10, 100, Filter{})
	if len(last) != 100 || last[99].Target != "host50:443" {
		t.Errorf("最后一页应止于最旧的未淘汰记录，实际 %d 条，末条 %s", len(last), last[len(last)-1].Target)
	}
	if beyond, _ := r.Page(11, 100, Filter{}); len(beyond) != 0 {
		t.Error("超出范围的页应为空")
	}
}

func TestRingFilter(t *testing.T) {
	r := NewRing()
	r.Add(Record{At: time.Now(), Node: "51:10002:vless", Name: "DMIT-JP-szhhaoo", Machine: "DMIT-JP", Protocol: "vless", Target: "claude.ai:443"})
	r.Add(Record{At: time.Now(), Node: "51:22023:ss", Name: "DMIT-JP:22023", Machine: "DMIT-JP", Protocol: "shadowsocks", Target: "x.com:443"})
	r.Add(Record{At: time.Now(), Node: "16:21002:ss", Name: "Lam-JP:21002", Machine: "Lam-JP", Protocol: "shadowsocks", Target: "b.com:443"})
	if got, n := r.Page(1, 100, Filter{Query: "CLAUDE"}); n != 1 || got[0].Name != "DMIT-JP-szhhaoo" {
		t.Errorf("按网址筛选错: %d", n)
	}
	if _, n := r.Page(1, 100, Filter{Query: "22023"}); n != 1 {
		t.Errorf("按节点筛选错: %d", n)
	}
	if _, n := r.Page(1, 100, Filter{Machine: "Lam-JP"}); n != 1 {
		t.Errorf("按机器筛选错: %d", n)
	}
	if _, n := r.Page(1, 100, Filter{Machine: "DMIT-JP", Query: "shadowsocks"}); n != 1 {
		t.Errorf("机器和关键字要同时生效: %d", n)
	}
}

// DMIT-JP 上用户自建 sing-box 的真实布局：-c 只有 log 段，入站在 -C 目录里。
func TestMergeConfigsAcrossFileAndDirectory(t *testing.T) {
	cfg, err := MergeConfigs([]ConfigFile{
		{Path: "/etc/sing-box/conf.d/DMIT-JP.json", FromDir: true, Data: []byte(`{"inbounds":[{"type":"shadowsocks","tag":"ss2022-DMIT-JP","listen":"0.0.0.0","listen_port":22023,"method":"2022-blake3-aes-128-gcm","password":"x"}]}`)},
		{Path: "/etc/sing-box/config.json", Data: []byte(`{"log":{"level":"warn","timestamp":true}}`)},
		{Path: "/etc/sing-box/conf.d/zz.json", FromDir: true, Data: []byte(`{"log":{"level":"info"}}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Inbounds) != 1 || cfg.Inbounds[0] != (InboundDef{Tag: "ss2022-DMIT-JP", Type: "shadowsocks", Port: 22023}) {
		t.Errorf("入站解析错: %+v", cfg.Inbounds)
	}
	if cfg.Log.Level != "info" || cfg.Log.Timestamp == nil || !*cfg.Log.Timestamp || !cfg.Log.Verbose() {
		t.Errorf("-C 目录里的 log 应盖过 -c 里的，其余字段保留: %+v", cfg.Log)
	}
}

func TestLogVerbosity(t *testing.T) {
	for _, c := range []struct {
		log  LogSettings
		want bool
	}{
		{LogSettings{}, true},
		{LogSettings{Level: "info"}, true},
		{LogSettings{Level: "debug"}, true},
		{LogSettings{Level: "warn"}, false},
		{LogSettings{Level: "error"}, false},
		{LogSettings{Level: "info", Disabled: true}, false},
	} {
		if got := c.log.Verbose(); got != c.want {
			t.Errorf("%+v: Verbose()=%v，应为 %v", c.log, got, c.want)
		}
	}
}

func TestMergeConfigsReportsBrokenFile(t *testing.T) {
	if _, err := MergeConfigs([]ConfigFile{{Path: "/x.json", Data: []byte(`{"inbounds":[`)}}); err == nil {
		t.Error("坏配置应报错，不能当成没有入站")
	}
}

func TestParseColouredConsoleLine(t *testing.T) {
	line := "\x1b[36mINFO\x1b[0m[0042] [\x1b[38;5;99m7\x1b[0m 1ms] inbound/shadowsocks[ss]: inbound connection to \x1b[0mx.com:443"
	ev, ok := ParseLine(line)
	if !ok || ev.ConnID != 7 || ev.Addr != "x.com:443" {
		t.Fatalf("带颜色的控制台输出解析错: %+v %v", ev, ok)
	}
}

func TestRingUntilFreezesTheList(t *testing.T) {
	r := NewRing()
	r.Add(Record{At: time.Now(), Node: "n", Target: "a.com:443"})
	r.Add(Record{At: time.Now(), Node: "n", Target: "b.com:443"})
	frozen, _ := r.Page(1, 100, Filter{})
	r.Add(Record{At: time.Now(), Node: "n", Target: "c.com:443"})
	got, total := r.Page(1, 100, Filter{Until: frozen[0].ID})
	if total != 2 || got[0].Target != "b.com:443" {
		t.Errorf("暂停后新来的记录不该出现: %d %+v", total, got)
	}
}
