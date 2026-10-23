package service

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kukumi1/fluxlite/internal/audit"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/singbox"
	"github.com/kukumi1/fluxlite/internal/sshx"
)

const (
	// connAuditTimeout bounds one machine's read. It is generous because a
	// single command carries every log the machine is read for.
	connAuditTimeout = 10 * time.Second
	// connAuditReadLimit caps one round's read of one log, so a backlog after
	// an outage arrives over several rounds instead of in one huge reply.
	connAuditReadLimit = 1 << 20
	// connAuditTruncateAt is when a fully read panel node log is emptied.
	// Nothing else reads it, and sing-box appends, so truncating cannot
	// corrupt it.
	connAuditTruncateAt = 20 << 20
	// configReadLimit caps one config file. Configs are read every round:
	// that is what lets a node added on the machine appear without anyone
	// telling the panel.
	configReadLimit = 256 << 10
	// auditLogCheckEvery is how often a panel node's log section is
	// re-verified. Fixing it restarts the node, so this must stay rare.
	auditLogCheckEvery = 10 * time.Minute
	// connAuditConcurrency bounds how many machines are read at once.
	connAuditConcurrency = 6

	auditMarker = "__FLUXLITE_AUDIT__"
	// managedLogDir holds the logs of the nodes the panel created. Only logs
	// under it are ever truncated: any other belongs to whoever set that
	// sing-box up.
	managedLogDir = "/var/lib/fluxlite-singbox/"
	// logDropIn is the file the panel adds to an operator's -C directory to
	// raise its log level without touching their own files.
	logDropIn = "zz-fluxlite-audit-log.json"
)

var managedConfigPath = regexp.MustCompile(`^/etc/fluxlite/singbox/(\d+)/config\.json$`)

type fileCursor struct {
	inode    string
	offset   int64
	truncate bool
}

// logSource is one log the machine's sing-box processes write to, with where
// the last read stopped and the inbounds whose lines appear in it.
type logSource struct {
	id          string // "f:<path>" or "u:<unit>"
	path        string
	unit        string
	truncatable bool
	file        *fileCursor
	cursor      string
	corr        *audit.Correlator
	nodes       []*auditNode
}

// lookup finds the inbound a log line names. sing-box prints the inbound's
// type and tag; tags are unique within a process, so the pair settles it.
func (s *logSource) lookup(typ, tag string) *auditNode {
	for _, n := range s.nodes {
		if n.tag == tag && n.typ == typ {
			return n
		}
	}
	return nil
}

// auditNode is one inbound found running on a machine.
type auditNode struct {
	key      string
	name     string
	port     int
	protocol string
	tag      string
	typ      string
}

// machineAudit is what the audit keeps for one machine between rounds.
type machineAudit struct {
	client *sshx.Client
	// sources are the logs found last round; this round reads them from
	// their cursors while finding them afresh.
	sources map[string]*logSource
	// fixed holds the process configs whose log level the panel has tried to
	// raise, so a failing fix is not retried every few seconds.
	fixed map[string]bool
}

// connAudit is the traffic audit's state. All of it is in memory: the list is
// a live view, and a restart begins it afresh from the current log ends.
type connAudit struct {
	ring *audit.Ring

	mu         sync.Mutex
	machines   map[int64]*machineAudit
	names      []string
	logChecked map[int64]time.Time
}

func newConnAudit() *connAudit {
	return &connAudit{
		ring:       audit.NewRing(),
		machines:   make(map[int64]*machineAudit),
		logChecked: make(map[int64]time.Time),
	}
}

func (a *connAudit) machine(id int64) *machineAudit {
	m := a.machines[id]
	if m == nil {
		m = &machineAudit{sources: make(map[string]*logSource), fixed: make(map[string]bool)}
		a.machines[id] = m
	}
	return m
}

// TrafficAudit returns one page of the live audit list.
func (s *Service) TrafficAudit(page, size int, f audit.Filter) ([]audit.Record, int) {
	return s.audit.ring.Page(page, size, f)
}

// TrafficAuditMachines names the machines being audited, in panel order.
func (s *Service) TrafficAuditMachines() []string {
	s.audit.mu.Lock()
	defer s.audit.mu.Unlock()
	return slices.Clone(s.audit.names)
}

// CollectConnAudit reads every online machine once: which sing-box inbounds
// it runs and what their logs gained since the last read.
func (s *Service) CollectConnAudit(ctx context.Context) error {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	users, err := s.store.ListSingBoxUsers(ctx)
	if err != nil {
		return err
	}
	byID := make(map[int64]*model.Node, len(nodes))
	var machines []*model.Node
	for _, n := range nodes {
		byID[n.ID] = n
		if n.Status != model.StatusOffline {
			machines = append(machines, n)
		}
	}
	s.ensureAuditLogs(ctx, byID, users)

	managed := make(map[int64]*model.SingBoxUser, len(users))
	for _, u := range users {
		if u.Source == model.SingBoxManaged {
			managed[u.ID] = u
		}
	}
	outputs := s.readMachines(ctx, machines)

	a := s.audit
	a.mu.Lock()
	a.names = a.names[:0]
	for _, n := range machines {
		a.names = append(a.names, n.Name)
	}
	now := time.Now()
	var fixes []logFix
	for _, n := range machines {
		if out := outputs[n.ID]; out != nil {
			fixes = append(fixes, a.apply(n, out, managed, now)...)
		}
	}
	a.mu.Unlock()

	for _, f := range fixes {
		if err := s.raiseLogLevel(ctx, f); err != nil {
			s.log.Warn("raise sing-box log level", "node", f.node.Name, "config", f.key, "error", err)
		} else {
			s.log.Info("sing-box log level raised to info", "node", f.node.Name, "config", f.key)
		}
	}
	return nil
}

// readMachines runs each machine's command on the audit's own connection.
//
// Not the shared pool: a command that outlives its deadline closes the whole
// connection it ran on, and this runs every few seconds. On the pool, one slow
// machine would keep tearing down the connection that route sampling and
// traffic collection are using at the same moment.
func (s *Service) readMachines(ctx context.Context, machines []*model.Node) map[int64]*auditOutput {
	a := s.audit
	a.mu.Lock()
	cmds := make(map[int64]string, len(machines))
	keep := make(map[int64]bool, len(machines))
	for _, n := range machines {
		keep[n.ID] = true
		cmds[n.ID] = auditCommand(a.machine(n.ID))
	}
	for id, m := range a.machines {
		if !keep[id] {
			if m.client != nil {
				m.client.Close()
			}
			delete(a.machines, id)
		}
	}
	a.mu.Unlock()

	var (
		wg      sync.WaitGroup
		outMu   sync.Mutex
		outputs = make(map[int64]*auditOutput, len(machines))
		slots   = make(chan struct{}, connAuditConcurrency)
	)
	for _, n := range machines {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			out, err := s.runAudit(ctx, n, cmds[n.ID])
			if err != nil {
				s.log.Debug("traffic audit read failed", "node", n.Name, "error", err)
				return
			}
			outMu.Lock()
			outputs[n.ID] = parseAuditOutput(out)
			outMu.Unlock()
		}()
	}
	wg.Wait()
	return outputs
}

func (s *Service) runAudit(ctx context.Context, node *model.Node, cmd string) (string, error) {
	a := s.audit
	a.mu.Lock()
	client := a.machine(node.ID).client
	a.mu.Unlock()
	if client == nil {
		dialCtx, cancel := context.WithTimeout(ctx, connAuditTimeout)
		c, err := s.pool.Dedicated(dialCtx, node)
		cancel()
		if err != nil {
			return "", err
		}
		client = c
		a.mu.Lock()
		a.machine(node.ID).client = client
		a.mu.Unlock()
	}

	runCtx, cancel := context.WithTimeout(ctx, connAuditTimeout)
	defer cancel()
	res, err := sshx.Run(runCtx, client.Client, cmd)
	if err != nil {
		client.Close()
		a.mu.Lock()
		if m := a.machines[node.ID]; m != nil && m.client == client {
			m.client = nil
		}
		a.mu.Unlock()
		return "", err
	}
	return res.Stdout, nil
}

// discoverScript lists the machine's sing-box processes with what decides
// their nodes and their log: working directory, cgroup, stderr, executable,
// and every config file each loads, in the order sing-box loads them.
//
// Processes are found by /proc/*/comm rather than pgrep: busybox's pgrep -x
// matches nothing on some Alpine machines.
const discoverScript = `echo "$M DISC"
cfgfile() { p=$2; case "$p" in /*) ;; *) p="$cwd/$p";; esac
if [ -f "$p" ]; then echo "$M CFG $1 $p"; head -c ` + "%d" + ` "$p" 2>/dev/null; echo; echo "$M CFGEND"; else echo "$M CFGMISS $p"; fi; }
cfgdir() { d=$1; case "$d" in /*) ;; *) d="$cwd/$d";; esac
echo "$M CFGDIR $d"; for p in "$d"/*.json; do [ -f "$p" ] && cfgfile d "$p"; done; }
for proc in /proc/[0-9]*; do
[ "$(cat "$proc/comm" 2>/dev/null)" = sing-box ] && [ -r "$proc/cmdline" ] || continue
cwd=$(readlink "$proc/cwd" 2>/dev/null)
echo "$M PROC ${proc#/proc/}"
echo "$M CWD $cwd"
echo "$M EXE $(readlink "$proc/exe" 2>/dev/null)"
sed "s/^/$M CG /" "$proc/cgroup" 2>/dev/null
t=$(readlink "$proc/fd/2" 2>/dev/null); if [ -f "$t" ]; then echo "$M FD2F $t"; else echo "$M FD2 $t"; fi
tr '\0' '\n' < "$proc/cmdline" | { want=; any=
while IFS= read -r a; do
case "$want" in
c) cfgfile c "$a"; want=; any=1; continue;;
C) cfgdir "$a"; want=; any=1; continue;;
esac
case "$a" in
-c|--config) want=c;;
-C|--config-directory) want=C;;
--config=*) cfgfile c "${a#*=}"; any=1;;
--config-directory=*) cfgdir "${a#*=}"; any=1;;
esac
done
[ -n "$any" ] || cfgfile c config.json; }
done
echo "$M DEND"
`

// auditCommand is one machine's read: its sing-box processes, and every log
// found last round from where its last read stopped.
func auditCommand(m *machineAudit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "M=%s\n", auditMarker)
	fmt.Fprintf(&b, discoverScript, configReadLimit)

	ids := make([]string, 0, len(m.sources))
	for id := range m.sources {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		src := m.sources[id]
		if src.unit != "" {
			after := "-n 1"
			if src.cursor != "" {
				after = "--after-cursor=" + shellQuote(src.cursor)
			}
			fmt.Fprintf(&b, "echo \"$M UNIT\" %s\nout=$(journalctl -u %s %s -o short-iso-precise --show-cursor --no-pager 2>/dev/null); rc=$?\n"+
				"printf '%%s\\n' \"$out\" | tail -n 5000; echo \"$M RC $rc\"\n", shellQuote(id), shellQuote(src.unit), after)
			continue
		}
		fmt.Fprintf(&b, "echo \"$M FILE\" %s\nf=%s\n", shellQuote(id), shellQuote(src.path))
		b.WriteString(fileReadScript(src.file, src.truncatable))
	}
	return b.String()
}

// fileReadScript reads the log named by $f from cur onwards. Without a cursor
// it only reports where the file ends, so history is never replayed.
func fileReadScript(cur *fileCursor, truncatable bool) string {
	var b strings.Builder
	if cur != nil && cur.truncate && truncatable {
		fmt.Fprintf(&b, "[ \"$(stat -c '%%i %%s' \"$f\" 2>/dev/null)\" = %s ] && : > \"$f\"\n",
			shellQuote(cur.inode+" "+strconv.FormatInt(cur.offset, 10)))
	}
	b.WriteString("if s=$(stat -c '%i %s' \"$f\" 2>/dev/null); then echo \"$M STAT $s\"; ino=${s% *}; sz=${s#* }\n")
	if cur != nil {
		fmt.Fprintf(&b, "if [ \"$ino\" = %s ] && [ \"$sz\" -ge %d ]; then tail -c +%d \"$f\"; else cat \"$f\"; fi | head -c %d\n",
			shellQuote(cur.inode), cur.offset, cur.offset+1, connAuditReadLimit)
	}
	// The echo ends a last partial line so the marker after it starts a line
	// of its own; the parser removes exactly that newline again.
	b.WriteString("echo; fi\necho \"$M END\"\n")
	return b.String()
}

// procReport is one sing-box process as the discovery script saw it.
type procReport struct {
	pid      string
	cwd      string
	exe      string
	cgroup   []string
	stderr   string
	stderrOK bool // stderr is a regular file
	configs  []audit.ConfigFile
	// args are the -c files and -C directories in command-line order, which
	// is what a config check has to be given again.
	args    []configArg
	missing []string
}

type configArg struct {
	dir  bool
	path string
}

type fileChunk struct {
	stat  bool
	inode string
	size  int64
	data  string
}

type journalChunk struct {
	lines  []string
	cursor string
	rc     int
	rcSeen bool
}

type auditOutput struct {
	discovered bool
	procs      []*procReport
	files      map[string]*fileChunk
	journals   map[string]*journalChunk
}

func parseAuditOutput(out string) *auditOutput {
	res := &auditOutput{
		files:    make(map[string]*fileChunk),
		journals: make(map[string]*journalChunk),
	}
	var (
		proc    *procReport
		cfg     *audit.ConfigFile
		file    *fileChunk
		journal *journalChunk
		body    []string
	)
	closeBody := func() {
		switch {
		case cfg != nil:
			cfg.Data = []byte(strings.Join(body, "\n"))
			proc.configs = append(proc.configs, *cfg)
		case file != nil && file.stat:
			file.data = strings.Join(body, "\n")
		}
		cfg, file, body = nil, nil, nil
	}
	reset := func() {
		closeBody()
		proc, journal = nil, nil
	}

	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, auditMarker+" "); ok {
			verb, arg, _ := strings.Cut(rest, " ")
			switch verb {
			case "DISC":
				reset()
				res.discovered = true
			case "PROC":
				reset()
				proc = &procReport{pid: strings.TrimSpace(arg)}
				res.procs = append(res.procs, proc)
			case "DEND":
				reset()
			}
			if proc != nil {
				switch verb {
				case "CWD":
					proc.cwd = arg
				case "EXE":
					proc.exe = arg
				case "CG":
					proc.cgroup = append(proc.cgroup, arg)
				case "FD2":
					proc.stderr = arg
				case "FD2F":
					proc.stderr, proc.stderrOK = arg, true
				case "CFGDIR":
					proc.args = append(proc.args, configArg{dir: true, path: arg})
				case "CFG":
					closeBody()
					kind, p, _ := strings.Cut(arg, " ")
					if kind == "c" {
						proc.args = append(proc.args, configArg{path: p})
					}
					cfg = &audit.ConfigFile{Path: p, FromDir: kind == "d"}
				case "CFGMISS":
					proc.missing = append(proc.missing, arg)
					proc.args = append(proc.args, configArg{path: arg})
				case "CFGEND":
					closeBody()
				}
				continue
			}
			switch verb {
			case "FILE":
				reset()
				file = &fileChunk{}
				res.files[arg] = file
			case "STAT":
				if file != nil {
					ino, size, _ := strings.Cut(strings.TrimSpace(arg), " ")
					n, err := strconv.ParseInt(size, 10, 64)
					file.stat, file.inode, file.size = err == nil, ino, n
				}
			case "END":
				closeBody()
			case "UNIT":
				reset()
				journal = &journalChunk{}
				res.journals[arg] = journal
			case "RC":
				if journal != nil {
					journal.rc, _ = strconv.Atoi(strings.TrimSpace(arg))
					journal.rcSeen = true
				}
				journal = nil
			}
			continue
		}
		switch {
		case cfg != nil, file != nil && file.stat:
			body = append(body, line)
		case journal != nil:
			if c, ok := strings.CutPrefix(line, "-- cursor: "); ok {
				journal.cursor = strings.TrimSpace(c)
			} else if line != "" {
				journal.lines = append(journal.lines, line)
			}
		}
	}
	closeBody()
	return res
}

// procLog is where a process's connection lines can be read, or why they
// cannot.
type procLog struct {
	cfg  audit.Config
	id   string // "" when unreadable
	path string
	unit string
	// detail names the log, or the reason it cannot be read.
	detail string
	// quiet is set when the config parsed but its level prints no
	// connections: the one problem the panel can fix.
	quiet bool
}

// resolveLog decides where a process's connection lines go: the file its log
// section names, else its stderr — a file under OpenRC, journald under
// systemd.
func resolveLog(p *procReport) procLog {
	if len(p.missing) > 0 {
		return procLog{detail: "读不到配置文件 " + strings.Join(p.missing, "、")}
	}
	cfg, err := audit.MergeConfigs(p.configs)
	if err != nil {
		return procLog{detail: "配置解析失败：" + err.Error()}
	}
	l := procLog{cfg: cfg}
	switch {
	case cfg.Log.Disabled:
		l.detail, l.quiet = "配置里关掉了日志（log.disabled），看不到连接", true
	case !cfg.Log.Verbose():
		l.detail, l.quiet = "日志级别是 "+cfg.Log.Level+"，看不到连接，要改成 info", true
	case cfg.Log.Output != "":
		l.path = cfg.Log.Output
		if !path.IsAbs(l.path) {
			l.path = path.Join(p.cwd, l.path)
		}
		l.id, l.detail = "f:"+l.path, "日志文件 "+l.path
	case p.stderrOK:
		l.path = p.stderr
		l.id, l.detail = "f:"+l.path, "日志文件 "+l.path
	case serviceUnit(p.cgroup) != "":
		l.unit = serviceUnit(p.cgroup)
		l.id, l.detail = "u:"+l.unit, "journald（"+l.unit+"）"
	default:
		l.detail = "日志输出到 " + cmp.Or(p.stderr, "终端") + "，读不到"
	}
	return l
}

// serviceUnit is the systemd unit a process runs under, from its cgroup.
func serviceUnit(cgroup []string) string {
	for _, line := range cgroup {
		if i := strings.LastIndexByte(line, '/'); i >= 0 && strings.HasSuffix(line, ".service") {
			return line[i+1:]
		}
	}
	return ""
}

// openRCService is the OpenRC service a process runs under, from its cgroup.
func openRCService(cgroup []string) string {
	for _, line := range cgroup {
		if i := strings.Index(line, "/openrc."); i >= 0 {
			name, _, _ := strings.Cut(line[i+len("/openrc."):], "/")
			return name
		}
	}
	return ""
}

// managedID is the panel node a process serves, or 0 for one the operator
// runs themselves.
func managedID(p *procReport) int64 {
	for _, c := range p.configs {
		if m := managedConfigPath.FindStringSubmatch(c.Path); m != nil {
			id, _ := strconv.ParseInt(m[1], 10, 64)
			return id
		}
	}
	return 0
}

// configKey identifies a process's configuration across restarts.
func configKey(p *procReport) string {
	parts := make([]string, len(p.args))
	for i, a := range p.args {
		parts[i] = a.path
	}
	return strings.Join(parts, " ")
}

// logFix is a process whose log level hides its connections.
type logFix struct {
	node *model.Node
	proc *procReport
	cfg  audit.Config
	key  string
}

// apply reads one machine's logs into the list and rediscovers its sing-box
// processes. It runs under the audit lock.
func (a *connAudit) apply(node *model.Node, out *auditOutput, managed map[int64]*model.SingBoxUser, now time.Time) []logFix {
	m := a.machine(node.ID)

	for id, src := range m.sources {
		var conns []audit.Conn
		if src.unit != "" {
			conns = readJournal(src, out.journals[id])
		} else {
			conns = readFile(src, out.files[id])
		}
		for _, conn := range conns {
			if !isUDPOverTCP(conn.To) {
				a.ring.Add(nodeRecord(node, src, conn))
			}
		}
		src.corr.Prune(now)
	}

	if !out.discovered {
		return nil
	}
	return a.rediscover(node, m, out.procs, managed)
}

// rediscover rebuilds a machine's nodes and log sources from this round's
// processes, carrying each surviving log's cursor over.
func (a *connAudit) rediscover(node *model.Node, m *machineAudit, procs []*procReport, managed map[int64]*model.SingBoxUser) []logFix {
	sources := make(map[string]*logSource)
	var fixes []logFix
	for _, p := range procs {
		l := resolveLog(p)
		if l.quiet && managedID(p) == 0 {
			key := configKey(p)
			if !m.fixed[key] {
				m.fixed[key] = true
				fixes = append(fixes, logFix{node: node, proc: p, cfg: l.cfg, key: key})
			}
		}
		if l.id == "" {
			continue
		}
		user := managed[managedID(p)]
		src := sources[l.id]
		if src == nil {
			src = m.sources[l.id]
			if src == nil {
				src = &logSource{id: l.id, path: l.path, unit: l.unit, corr: audit.NewCorrelator()}
			}
			src.truncatable = user != nil && strings.HasPrefix(l.path, managedLogDir)
			src.nodes = nil
			sources[l.id] = src
		}
		for _, in := range l.cfg.Inbounds {
			if in.Port <= 0 {
				continue
			}
			n := &auditNode{
				key:      fmt.Sprintf("%d:%d:%s", node.ID, in.Port, in.Tag),
				name:     fmt.Sprintf("%s:%d", node.Name, in.Port),
				port:     in.Port,
				protocol: in.Type,
				tag:      in.Tag,
				typ:      in.Type,
			}
			if user != nil {
				n.name = user.Name
			}
			src.nodes = append(src.nodes, n)
		}
	}
	m.sources = sources
	return fixes
}

// isUDPOverTCP reports sing-box's UDP-over-TCP placeholder destinations. A
// client tunnelling UDP names one of them instead of a real site; the real
// destinations follow inside the tunnel.
func isUDPOverTCP(target string) bool {
	switch audit.HostOf(target) {
	case "sp.v2.udp-over-tcp.arpa", "sp.udp-over-tcp.arpa":
		return true
	}
	return false
}

// nodeRecord is the list row for a connection a machine's sing-box served.
func nodeRecord(node *model.Node, src *logSource, conn audit.Conn) audit.Record {
	rec := audit.Record{At: conn.At, Machine: node.Name, Target: conn.To}
	if n := src.lookup(conn.InboundType, conn.Inbound); n != nil {
		rec.Node, rec.Name, rec.Port, rec.Protocol = n.key, n.name, n.port, n.protocol
	} else {
		rec.Node = fmt.Sprintf("%d:0:%s", node.ID, conn.Inbound)
		rec.Name, rec.Protocol = node.Name+" "+cmp.Or(conn.Inbound, conn.InboundType), conn.InboundType
	}
	return rec
}

// readFile advances a log file's cursor past what chunk returned and yields
// the completed connections in it. The first sighting of a file only records
// where it ends: history from before the panel started is not replayed.
func readFile(src *logSource, chunk *fileChunk) []audit.Conn {
	if chunk == nil || !chunk.stat {
		return nil
	}
	cur := src.file
	if cur == nil {
		src.file = &fileCursor{inode: chunk.inode, offset: chunk.size}
		return nil
	}
	base := int64(0)
	if cur.inode == chunk.inode && chunk.size >= cur.offset {
		base = cur.offset
	}
	consumed := strings.LastIndexByte(chunk.data, '\n') + 1
	cur.inode, cur.offset = chunk.inode, base+int64(consumed)
	cur.truncate = src.truncatable && cur.offset == chunk.size && chunk.size > connAuditTruncateAt
	return feedLines(src.corr, strings.Split(chunk.data[:consumed], "\n"), time.Now())
}

// readJournal advances a journal cursor. A unit read for the first time, or a
// cursor journald no longer accepts, starts over from the journal's end.
func readJournal(src *logSource, chunk *journalChunk) []audit.Conn {
	if chunk == nil || !chunk.rcSeen || chunk.rc != 0 {
		src.cursor = ""
		return nil
	}
	fresh := src.cursor == ""
	if chunk.cursor != "" {
		src.cursor = chunk.cursor
	}
	if fresh {
		return nil
	}
	return feedLines(src.corr, chunk.lines, time.Now())
}

func feedLines(c *audit.Correlator, lines []string, now time.Time) []audit.Conn {
	var conns []audit.Conn
	for _, line := range lines {
		ev, ok := audit.ParseLine(line)
		if !ok {
			continue
		}
		if conn, ok := c.Feed(ev, now); ok {
			conns = append(conns, conn)
		}
	}
	return conns
}

// raiseLogLevel makes an operator's own sing-box log connections. With a -C
// directory it adds a file there and leaves theirs alone; otherwise it edits
// the log level in the -c file that sets it, keeping a backup. Either way the
// result is checked by sing-box before the one service is restarted.
func (s *Service) raiseLogLevel(ctx context.Context, f logFix) error {
	var restart string
	switch unit, rc := serviceUnit(f.proc.cgroup), openRCService(f.proc.cgroup); {
	case unit != "":
		restart = "systemctl restart " + shellQuote(unit)
	case rc != "":
		restart = "rc-service " + shellQuote(rc) + " restart"
	default:
		return fmt.Errorf("no service manager owns this sing-box, so it cannot be restarted")
	}
	bin := cmp.Or(f.proc.exe, "sing-box")

	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	client, err := s.pool.Get(ctx, f.node)
	if err != nil {
		return err
	}

	var check strings.Builder
	check.WriteString(shellQuote(bin) + " check")
	if f.proc.cwd != "" {
		check.WriteString(" -D " + shellQuote(f.proc.cwd))
	}
	var lastDir string
	for _, arg := range f.proc.args {
		flag := " -c "
		if arg.dir {
			flag, lastDir = " -C ", arg.path
		}
		check.WriteString(flag + shellQuote(arg.path))
	}

	var cmd string
	if lastDir != "" {
		section := map[string]any{"level": "info", "disabled": false}
		if f.cfg.Log.Output != "" {
			section["output"] = f.cfg.Log.Output
		}
		if f.cfg.Log.Timestamp != nil {
			section["timestamp"] = *f.cfg.Log.Timestamp
		}
		data, err := json.MarshalIndent(map[string]any{"log": section}, "", "  ")
		if err != nil {
			return err
		}
		target := path.Join(lastDir, logDropIn)
		if err := sshx.WriteFile(ctx, client.Client, target, data, "0640"); err != nil {
			return err
		}
		cmd = "t=" + shellQuote(target) + "\n" +
			"chown \"$(stat -c '%u:%g' " + shellQuote(lastDir) + ")\" \"$t\" && " + check.String() + " >/dev/null || { rm -f \"$t\"; exit 1; }\n" +
			restart
	} else {
		var file *audit.ConfigFile
		for i := range f.proc.configs {
			c := &f.proc.configs[i]
			if file == nil || strings.Contains(string(c.Data), `"log"`) {
				file = c
			}
		}
		if file == nil {
			return fmt.Errorf("no config file to edit")
		}
		next, err := singbox.WithInfoLog(file.Data)
		if err != nil {
			return err
		}
		tmp := file.Path + ".fluxlite-audit-new"
		if err := sshx.WriteFile(ctx, client.Client, tmp, next, "0600"); err != nil {
			return err
		}
		checked := strings.Replace(check.String(), " -c "+shellQuote(file.Path), " -c "+shellQuote(tmp), 1)
		cmd = "f=" + shellQuote(file.Path) + "; t=" + shellQuote(tmp) + "\n" +
			"chown \"$(stat -c '%u:%g' \"$f\")\" \"$t\" && chmod \"$(stat -c '%a' \"$f\")\" \"$t\" && " + checked + " >/dev/null || { rm -f \"$t\"; exit 1; }\n" +
			"[ -e \"$f.bak-fluxlite-audit\" ] || cp -p \"$f\" \"$f.bak-fluxlite-audit\"\n" +
			"mv \"$t\" \"$f\" && " + restart
	}
	if _, err := sshx.RunCheck(ctx, client.Client, cmd); err != nil {
		return fmt.Errorf("check or restart sing-box: %w", err)
	}
	return nil
}

// ensureAuditLogs makes sure every managed node logs connections, which its
// generated config did not ask for before the audit existed.
func (s *Service) ensureAuditLogs(ctx context.Context, byID map[int64]*model.Node, users []*model.SingBoxUser) {
	for _, u := range users {
		node, ok := byID[u.NodeID]
		if u.Source != model.SingBoxManaged || !ok || node.Status == model.StatusOffline {
			continue
		}
		a := s.audit
		a.mu.Lock()
		due := time.Since(a.logChecked[u.ID]) >= auditLogCheckEvery
		if due {
			a.logChecked[u.ID] = time.Now()
		}
		a.mu.Unlock()
		if !due {
			continue
		}
		if err := s.ensureSingBoxAuditLog(ctx, node, u); err != nil {
			s.log.Warn("enable sing-box connection log", "user", u.Name, "error", err)
		}
	}
}

func (s *Service) ensureSingBoxAuditLog(ctx context.Context, node *model.Node, u *model.SingBoxUser) error {
	dir := fmt.Sprintf("/etc/fluxlite/singbox/%d", u.ID)
	if u.ServiceName != fmt.Sprintf("fluxlite-singbox-%d", u.ID) || u.ConfigPath != dir+"/config.json" {
		return fmt.Errorf("refusing to edit %s outside the managed namespace", u.ConfigPath)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return err
	}
	raw, err := sshx.RunCheck(ctx, client.Client, "cat "+shellQuote(u.ConfigPath))
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	next, changed, err := singbox.WithAuditLog([]byte(raw), u.ID)
	if err != nil || !changed {
		return err
	}

	tmp := u.ConfigPath + ".audit-new"
	if err := sshx.WriteFile(ctx, client.Client, tmp, next, "0600"); err != nil {
		return err
	}
	data := fmt.Sprintf("/var/lib/fluxlite-singbox/%d", u.ID)
	// The replacement takes the current file's owner rather than the service
	// account's name: nodes created before per-node accounts run as root and
	// have no such user to hand the file to.
	cmd := "bin=$(command -v sing-box || echo /usr/local/bin/sing-box)\n" +
		"if ! \"$bin\" check -D " + shellQuote(data) + " -c " + shellQuote(tmp) +
		" || ! chown \"$(stat -c '%u:%g' " + shellQuote(u.ConfigPath) + ")\" " + shellQuote(tmp) +
		"; then rm -f " + shellQuote(tmp) + "; exit 1; fi\n" +
		"mv " + shellQuote(tmp) + " " + shellQuote(u.ConfigPath)
	if u.Enabled {
		switch node.InitSystem {
		case model.InitSystemd:
			cmd += " && systemctl restart " + shellQuote(u.ServiceName+".service")
		case model.InitOpenRC:
			cmd += " && rc-service " + shellQuote(u.ServiceName) + " restart"
		}
	}
	if _, err := sshx.RunCheck(ctx, client.Client, cmd); err != nil {
		return fmt.Errorf("apply connection log: %w", err)
	}
	s.log.Info("sing-box connection log enabled", "user", u.Name, "node", node.Name)
	return nil
}
