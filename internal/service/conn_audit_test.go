package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kukumi1/fluxlite/internal/audit"
	"github.com/kukumi1/fluxlite/internal/model"
)

func vlessLine(id int, dir, addr string) string {
	return "+0900 2026-10-22 19:17:41 INFO [" + strconv.Itoa(id) + " 0ms] inbound/vless[fluxlite-dmit-jp-szhhaoo]: [DMIT-JP-szhhaoo] inbound connection " + dir + " " + addr + "\n"
}

func ssJournalLine(id int, dir, addr string) string {
	return "2026-10-22T18:56:39.930740+09:00 DMIT-NCT6cYuztT sing-box[905]: +0900 2026-10-22 18:56:39 INFO [" + strconv.Itoa(id) +
		" 9ms] inbound/shadowsocks[ss2022-DMIT-JP]: inbound connection " + dir + " " + addr
}

// DMIT-JP 上真实的两个 sing-box：用户自建的（-c 只有 log 段、入站在 -C 目录、
// journald），和面板建的节点 27（写自己的日志文件）。
func dmitDiscovery() string {
	m := auditMarker + " "
	return strings.Join([]string{
		m + "DISC",
		m + "PROC 905",
		m + "CWD /var/lib/sing-box",
		m + "EXE /usr/bin/sing-box",
		m + "CG 0::/system.slice/sb-sing-box.service",
		m + "FD2 socket:[7021]",
		m + "CFG c /etc/sing-box/config.json",
		`{"log":{"level":"info","timestamp":true}}`,
		"",
		m + "CFGEND",
		m + "CFGDIR /etc/sing-box/conf.d",
		m + "CFG d /etc/sing-box/conf.d/DMIT-JP.json",
		`{"inbounds":[{"type":"shadowsocks","tag":"ss2022-DMIT-JP","listen":"0.0.0.0","listen_port":22023,"method":"2022-blake3-aes-128-gcm","password":"x"}]}`,
		"",
		m + "CFGEND",
		m + "PROC 843997",
		m + "CWD /var/lib/fluxlite-singbox/27",
		m + "EXE /usr/bin/sing-box",
		m + "CG 0::/system.slice/fluxlite-singbox-27.service",
		m + "FD2 socket:[2924254]",
		m + "CFG c /etc/fluxlite/singbox/27/config.json",
		`{"log":{"level":"info","output":"/var/lib/fluxlite-singbox/27/access.log","timestamp":true},`,
		`"inbounds":[{"type":"vless","tag":"fluxlite-dmit-jp-szhhaoo","listen":"::","listen_port":10002,"users":[{"name":"DMIT-JP-szhhaoo"}]}]}`,
		"",
		m + "CFGEND",
		m + "DEND",
	}, "\n") + "\n"
}

var (
	dmitNode = &model.Node{ID: 51, Name: "DMIT-JP", Host: "191.222.209.254"}
	dmitUser = map[int64]*model.SingBoxUser{27: {ID: 27, Name: "DMIT-JP-szhhaoo", Source: model.SingBoxManaged}}
)

const (
	selfBuiltKey = "51:22023:ss2022-DMIT-JP"
	managedKey   = "51:10002:fluxlite-dmit-jp-szhhaoo"
	journalID    = "u:sb-sing-box.service"
	managedLogID = "f:/var/lib/fluxlite-singbox/27/access.log"
)

func TestParseDiscoveryOfBothProcesses(t *testing.T) {
	res := parseAuditOutput(dmitDiscovery())
	if !res.discovered || len(res.procs) != 2 {
		t.Fatalf("发现段解析错: %+v", res)
	}
	p := res.procs[0]
	if p.pid != "905" || p.cwd != "/var/lib/sing-box" || len(p.configs) != 2 || !p.configs[1].FromDir {
		t.Fatalf("自建进程解析错: %+v", p)
	}
	if len(p.args) != 2 || p.args[0] != (configArg{path: "/etc/sing-box/config.json"}) || p.args[1] != (configArg{dir: true, path: "/etc/sing-box/conf.d"}) {
		t.Errorf("命令行里的 -c / -C 要按原顺序保留，校验时原样带上: %+v", p.args)
	}
	if serviceUnit(p.cgroup) != "sb-sing-box.service" || p.stderrOK {
		t.Errorf("unit 或 stderr 解析错: %v %q", p.cgroup, p.stderr)
	}
	if managedID(p) != 0 || managedID(res.procs[1]) != 27 {
		t.Error("面板节点要靠配置路径认出来，自建的不能误认")
	}
}

func TestRediscoverNamesEachInboundByItsPort(t *testing.T) {
	a := newConnAudit()
	if fixes := a.apply(dmitNode, parseAuditOutput(dmitDiscovery()), dmitUser, time.Now()); len(fixes) != 0 {
		t.Fatalf("两个都是 info，不该改配置: %+v", fixes)
	}
	m := a.machines[51]
	managed, self := m.sources[managedLogID], m.sources[journalID]
	if managed == nil || !managed.truncatable || len(managed.nodes) != 1 {
		t.Fatalf("面板节点的日志来源错（它自己的日志可以截断）: %+v", managed)
	}
	if n := managed.nodes[0]; n.key != managedKey || n.name != "DMIT-JP-szhhaoo" || n.port != 10002 || n.protocol != "vless" {
		t.Errorf("面板节点错: %+v", n)
	}
	if self == nil || self.truncatable || len(self.nodes) != 1 {
		t.Fatalf("自建节点要读 journald，且不归面板截断: %+v", self)
	}
	if n := self.nodes[0]; n.key != selfBuiltKey || n.name != "DMIT-JP:22023" || n.protocol != "shadowsocks" {
		t.Errorf("自建节点错: %+v", n)
	}
}

// 两个节点同时有人用：各自访问的网站只记到各自名下。
func TestConnectionsGoToTheirOwnNode(t *testing.T) {
	a := newConnAudit()
	now := time.Now()
	a.apply(dmitNode, parseAuditOutput(dmitDiscovery()), dmitUser, now)
	// 第二轮：两个来源都第一次读，只定位到末尾。
	m := auditMarker + " "
	locate := dmitDiscovery() + strings.Join([]string{
		m + "UNIT " + journalID, "-- cursor: c1", m + "RC 0",
		m + "FILE " + managedLogID, m + "STAT 77 100", "", m + "END",
	}, "\n")
	a.apply(dmitNode, parseAuditOutput(locate), dmitUser, now)

	round := dmitDiscovery() + strings.Join([]string{
		m + "UNIT " + journalID,
		ssJournalLine(1, "from", "111.55.152.150:29176"),
		ssJournalLine(1, "to", "www.youtube.com:443"),
		ssJournalLine(2, "from", "2.27.109.202:5544"),
		ssJournalLine(2, "to", "www.gstatic.com:443"),
		ssJournalLine(3, "from", "152.53.54.162:1"),
		ssJournalLine(4, "from", "111.55.152.150:29177"),
		ssJournalLine(4, "to", "sp.v2.udp-over-tcp.arpa:0"),
		"-- cursor: c2",
		m + "RC 0",
		m + "FILE " + managedLogID,
		m + "STAT 77 400",
		strings.TrimSuffix(vlessLine(9, "from", "[2408:8207::5]:4431")+vlessLine(9, "to", "claude.ai:443"), "\n"),
		"",
		m + "END",
	}, "\n")
	a.apply(dmitNode, parseAuditOutput(round), dmitUser, now)

	recs, total := a.ring.Page(1, 100, audit.Filter{})
	if total != 3 {
		t.Fatalf("应有 3 条（只建连不发目标的探测、UDP-over-TCP 占位地址都不算），实际 %d: %+v", total, recs)
	}
	byTarget := map[string]audit.Record{}
	for _, r := range recs {
		byTarget[r.Target] = r
	}
	for _, target := range []string{"www.youtube.com:443", "www.gstatic.com:443"} {
		if r := byTarget[target]; r.Node != selfBuiltKey || r.Name != "DMIT-JP:22023" || r.Machine != "DMIT-JP" || r.Port != 22023 || r.Protocol != "shadowsocks" {
			t.Errorf("自建节点的记录错: %+v", r)
		}
	}
	if r := byTarget["claude.ai:443"]; r.Node != managedKey || r.Name != "DMIT-JP-szhhaoo" || r.Port != 10002 || r.Protocol != "vless" {
		t.Errorf("面板节点的记录错: %+v", r)
	}
}

func TestNewInboundAppearsAndRemovedOneDisappears(t *testing.T) {
	a := newConnAudit()
	now := time.Now()
	more := strings.Replace(dmitDiscovery(), `"password":"x"}]`, `"password":"x"},{"type":"hysteria2","tag":"hy2","listen_port":22024}]`, 1)
	a.apply(dmitNode, parseAuditOutput(more), dmitUser, now)
	if got := len(a.machines[51].sources[journalID].nodes); got != 2 {
		t.Fatalf("新加的端口应自动认出来: %d", got)
	}
	only := auditMarker + " DISC\n" + auditMarker + " DEND\n"
	a.apply(dmitNode, parseAuditOutput(only), dmitUser, now)
	if len(a.machines[51].sources) != 0 {
		t.Error("进程都停了，日志来源都应消失")
	}
}

func TestQuietSelfBuiltIsFixedOnceAndManagedNever(t *testing.T) {
	a := newConnAudit()
	quiet := strings.Replace(dmitDiscovery(), `"level":"info","timestamp"`, `"level":"warn","timestamp"`, 1)
	fixes := a.apply(dmitNode, parseAuditOutput(quiet), dmitUser, time.Now())
	if len(fixes) != 1 || fixes[0].proc.pid != "905" {
		t.Fatalf("自建节点是 warn 时应安排一次自动修复: %+v", fixes)
	}
	if again := a.apply(dmitNode, parseAuditOutput(quiet), dmitUser, time.Now()); len(again) != 0 {
		t.Error("同一份配置不能每轮都去改、反复重启")
	}
	if a.machines[51].sources[journalID] != nil {
		t.Error("看不到连接的日志不该当成来源去读")
	}

	b := newConnAudit()
	managedQuiet := strings.Replace(dmitDiscovery(), `"level":"info","output"`, `"level":"warn","output"`, 1)
	if fixes := b.apply(dmitNode, parseAuditOutput(managedQuiet), dmitUser, time.Now()); len(fixes) != 0 {
		t.Error("面板节点的配置由 ensureAuditLogs 管，这里不能动")
	}
}

func TestResolveLogFollowsWhereSingBoxWrites(t *testing.T) {
	cfg := func(log string) []audit.ConfigFile {
		return []audit.ConfigFile{{Path: "/etc/sb.json", Data: []byte(`{"log":` + log + `,"inbounds":[{"type":"vless","tag":"v","listen_port":1}]}`)}}
	}
	cases := []struct {
		name string
		p    procReport
		id   string
		ok   bool
	}{
		{"相对路径的 output 相对工作目录", procReport{cwd: "/var/lib/sb", configs: cfg(`{"output":"box.log"}`)}, "f:/var/lib/sb/box.log", true},
		{"OpenRC 把 stderr 重定向到文件", procReport{stderr: "/var/log/sing-box.log", stderrOK: true, configs: cfg(`{"level":"info"}`)}, "f:/var/log/sing-box.log", true},
		{"systemd 下进 journald", procReport{stderr: "socket:[1]", cgroup: []string{"0::/system.slice/sing-box.service"}, configs: cfg(`{}`)}, "u:sing-box.service", true},
		{"输出到终端", procReport{stderr: "/dev/pts/0", configs: cfg(`{}`)}, "", false},
		{"配置文件不见了", procReport{missing: []string{"/etc/x.json"}}, "", false},
	}
	for _, c := range cases {
		l := resolveLog(&c.p)
		if l.id != c.id || (l.id != "") != c.ok || l.detail == "" {
			t.Errorf("%s: %+v", c.name, l)
		}
	}
	if got := openRCService([]string{"0::/openrc.sing-box"}); got != "sing-box" {
		t.Errorf("OpenRC 服务名解析错: %q", got)
	}
}

// 在真 shell 里跑生成的命令，验证续读位置、半行、换 inode 和截断。只拼字符串的
// 测试抓不到 shell 语法或 stat/tail 参数写错。
func TestAuditFileReadThroughShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("没有 sh")
	}
	dir := t.TempDir()
	path := filepath.ToSlash(filepath.Join(dir, "access.log"))
	write := func(content string, appendMode bool) {
		flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		if appendMode {
			flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		f, err := os.OpenFile(path, flag, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(content); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}

	src := &logSource{id: "f:" + path, path: path, truncatable: true, corr: audit.NewCorrelator()}
	m := &machineAudit{sources: map[string]*logSource{src.id: src}}
	round := func() []audit.Conn {
		out, err := exec.Command(sh, "-c", auditCommand(m)).Output()
		if err != nil {
			t.Fatalf("命令执行失败: %v", err)
		}
		return readFile(src, parseAuditOutput(string(out)).files[src.id])
	}

	write(vlessLine(1, "from", "9.9.9.9:1")+vlessLine(1, "to", "old.example:443"), false)
	if conns := round(); len(conns) != 0 {
		t.Fatalf("面板启动前的旧日志不该回放: %v", conns)
	}

	write(vlessLine(2, "from", "1.2.3.4:5555")+vlessLine(2, "to", "claude.ai:443")+"+0000 2026-10-22 06:00:01 INFO [3 0ms] inbound/vl", true)
	conns := round()
	if len(conns) != 1 || conns[0].To != "claude.ai:443" || conns[0].From != "1.2.3.4:5555" || conns[0].InboundType != "vless" {
		t.Fatalf("新增连接读错: %+v", conns)
	}

	write("ess[fluxlite-dmit-jp-szhhaoo]: [DMIT-JP-szhhaoo] inbound connection from 5.6.7.8:1\n"+vlessLine(3, "to", "x.com:443"), true)
	conns = round()
	if len(conns) != 1 || conns[0].To != "x.com:443" || conns[0].From != "5.6.7.8:1" {
		t.Fatalf("半行续读错: %+v", conns)
	}
	if conns := round(); len(conns) != 0 {
		t.Fatalf("没有新内容却读出了记录: %+v", conns)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	write(vlessLine(4, "from", "1.1.1.1:1")+vlessLine(4, "to", "new.example:443"), false)
	conns = round()
	if len(conns) != 1 || conns[0].To != "new.example:443" {
		t.Fatalf("换文件后没从头读: %+v", conns)
	}

	src.file.truncate = true
	round()
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatalf("应已截断: %v %v", info.Size(), err)
	}
	write(vlessLine(5, "from", "2.2.2.2:1")+vlessLine(5, "to", "after.example:443"), true)
	conns = round()
	if len(conns) != 1 || conns[0].To != "after.example:443" {
		t.Fatalf("截断后没接着读: %+v", conns)
	}
}

func TestOnlyManagedLogsAreTruncated(t *testing.T) {
	src := &logSource{path: "/var/log/sb.log", corr: audit.NewCorrelator()}
	big := int64(connAuditTruncateAt + 1)
	readFile(src, &fileChunk{stat: true, inode: "5", size: big})
	data := vlessLine(2, "from", "1.1.1.1:1") + vlessLine(2, "to", "new.com:443")
	got := readFile(src, &fileChunk{stat: true, inode: "5", size: big + int64(len(data)), data: data})
	if len(got) != 1 || src.file.truncate {
		t.Errorf("自建 sing-box 的日志续读但不截断: %v %+v", got, src.file)
	}
	if script := auditCommand(&machineAudit{sources: map[string]*logSource{"f:/var/log/sb.log": src}}); strings.Contains(script, ": > ") {
		t.Error("自建 sing-box 的日志不归面板管，不能截断")
	}
}

func TestReadJournalStartsAtEndAndFollowsCursor(t *testing.T) {
	src := &logSource{unit: "sb-sing-box.service", corr: audit.NewCorrelator()}
	first := &journalChunk{cursor: "c1", rcSeen: true, lines: []string{ssJournalLine(1, "from", "1.1.1.1:1"), ssJournalLine(1, "to", "old.com:443")}}
	if got := readJournal(src, first); len(got) != 0 {
		t.Fatalf("第一次只定位到末尾，不该回放: %v", got)
	}
	next := &journalChunk{cursor: "c2", rcSeen: true, lines: []string{ssJournalLine(2, "from", "1.1.1.1:2"), ssJournalLine(2, "to", "new.com:443")}}
	if got := readJournal(src, next); len(got) != 1 || got[0].To != "new.com:443" || src.cursor != "c2" {
		t.Fatalf("游标之后的内容没读出来: %v %q", got, src.cursor)
	}
	if got := readJournal(src, &journalChunk{rcSeen: true}); len(got) != 0 || src.cursor != "c2" {
		t.Errorf("没有新内容时游标应保持不变")
	}
	readJournal(src, &journalChunk{rcSeen: true, rc: 1})
	if src.cursor != "" {
		t.Error("journald 拒绝游标（日志被轮转掉）时应重新定位")
	}
	if !strings.Contains(auditCommand(&machineAudit{sources: map[string]*logSource{"u:x": {unit: "x", cursor: "c9"}}}), "--after-cursor='c9'") {
		t.Error("已知的 journald 应从游标之后读")
	}
}
