package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/routeclass"
	"github.com/kukumi1/fluxlite/internal/sshx"
	"github.com/kukumi1/fluxlite/internal/store"
)

const (
	returnSettingsKey = "return.settings"

	// A return route is traced once a day; a trace that failed is retried
	// sooner, since the cause is often a machine that was briefly down.
	routeTraceEvery = 24 * time.Hour
	routeRetryAfter = 6 * time.Hour

	// natPortSpan is the widest port range still taken for a NAT machine
	// when the operator has not chosen which machines to trace. A NAT share
	// hands out a few forwarded ports; a whole machine is given thousands.
	natPortSpan = 20

	// returnTraceParallel bounds the traces a machine runs at once. Many
	// TCP traces in flight disturb each other's timing and rate limits.
	returnTraceParallel = 4
	returnTraceTimeout  = 60 * time.Second
	returnNodeTimeout   = 3 * time.Minute

	nextTraceDir = "/usr/local/lib/fluxlite"
)

// ReturnTarget is a return-route test point: a carrier node in one province
// that answers TCP on port 80, so a SYN trace always reaches it.
type ReturnTarget struct {
	Name    string `json:"name"`
	City    string `json:"city"`
	Carrier string `json:"carrier"`
	Host    string `json:"host"`
}

// ReturnPresets are Beijing, Shanghai, Guangdong and Sichuan on each of the
// three carriers.
func ReturnPresets() []ReturnTarget {
	cities := [][2]string{{"北京", "bj"}, {"上海", "sh"}, {"广东", "gd"}, {"四川", "sc"}}
	carriers := [][2]string{{"电信", "ct"}, {"联通", "cu"}, {"移动", "cm"}}
	var out []ReturnTarget
	for _, c := range cities {
		for _, k := range carriers {
			out = append(out, ReturnTarget{
				Name: c[0] + k[0], City: c[0], Carrier: k[0],
				Host: fmt.Sprintf("%s-%s-v4.ip.zstaticcdn.com", c[1], k[1]),
			})
		}
	}
	return out
}

// ReturnSettings chooses the test points and the machines that trace them.
type ReturnSettings struct {
	Targets   []string `json:"targets"`
	SkipNodes []int64  `json:"skip_nodes"`
}

var ErrReturnSettings = errors.New("回程设置无效")

// ReturnSettings returns the saved choice, or every test point traced from
// every machine but the NAT shares when nothing was saved yet.
func (s *Service) ReturnSettings(ctx context.Context) (ReturnSettings, error) {
	raw, ok, err := s.store.Setting(ctx, returnSettingsKey)
	if err != nil {
		return ReturnSettings{}, err
	}
	if ok {
		var cfg ReturnSettings
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return ReturnSettings{}, err
		}
		return cfg.normalized(), nil
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return ReturnSettings{}, err
	}
	cfg := ReturnSettings{}
	for _, p := range ReturnPresets() {
		cfg.Targets = append(cfg.Targets, p.Name)
	}
	for _, n := range nodes {
		if looksLikeNAT(n) {
			cfg.SkipNodes = append(cfg.SkipNodes, n.ID)
		}
	}
	return cfg.normalized(), nil
}

func looksLikeNAT(n *model.Node) bool {
	return n.PortEnd-n.PortStart+1 <= natPortSpan
}

func (c ReturnSettings) normalized() ReturnSettings {
	if c.Targets == nil {
		c.Targets = []string{}
	}
	if c.SkipNodes == nil {
		c.SkipNodes = []int64{}
	}
	return c
}

func (s *Service) SetReturnSettings(ctx context.Context, cfg ReturnSettings) error {
	known := map[string]bool{}
	for _, p := range ReturnPresets() {
		known[p.Name] = true
	}
	seen := map[string]bool{}
	for _, name := range cfg.Targets {
		if !known[name] || seen[name] {
			return ErrReturnSettings
		}
		seen[name] = true
	}
	if len(cfg.SkipNodes) > 1000 {
		return ErrReturnSettings
	}
	raw, err := json.Marshal(cfg.normalized())
	if err != nil {
		return err
	}
	return s.store.SetSetting(ctx, returnSettingsKey, string(raw))
}

// TraceTool supplies the tracer every machine runs.
type TraceTool interface {
	Binary(ctx context.Context, arch string) ([]byte, error)
	SHA256(arch string) (string, bool)
	Version() string
}

// SetTraceTool gives the service the tracer for return routes. Without one,
// return routes are reported as unavailable.
func (s *Service) SetTraceTool(t TraceTool) { s.traceTool = t }

type resolvedReturn struct {
	ReturnTarget
	ip string
}

func (s *Service) resolveReturnTargets(ctx context.Context, names []string) []resolvedReturn {
	var out []resolvedReturn
	for _, p := range ReturnPresets() {
		if !slices.Contains(names, p.Name) {
			continue
		}
		ip, err := resolveIPv4(ctx, p.Host)
		if err != nil {
			s.log.Warn("resolve return test point", "target", p.Name, "error", err)
			continue
		}
		out = append(out, resolvedReturn{ReturnTarget: p, ip: ip})
	}
	return out
}

func lastRoutes(routes []store.ReturnRoute) map[string]store.ReturnRoute {
	out := make(map[string]store.ReturnRoute, len(routes))
	for _, r := range routes {
		out[fmt.Sprintf("%d:%s", r.NodeID, r.Target)] = r
	}
	return out
}

// TraceReturnRoutes traces, from every online machine that is not skipped,
// the way back to each test point that is due: never traced, traced a day
// ago, or failed six hours ago. A line that changed since the last good
// trace is queued for the next alert digest.
func (s *Service) TraceReturnRoutes(ctx context.Context) error {
	if s.traceTool == nil {
		return nil
	}
	cfg, err := s.ReturnSettings(ctx)
	if err != nil || len(cfg.Targets) == 0 {
		return err
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	known, err := s.store.ReturnRoutes(ctx)
	if err != nil {
		return err
	}
	last := lastRoutes(known)
	now := time.Now()
	var targets []resolvedReturn
	for _, n := range nodes {
		if n.Status != model.StatusOnline || slices.Contains(cfg.SkipNodes, n.ID) || ctx.Err() != nil {
			continue
		}
		due := false
		for _, name := range cfg.Targets {
			prev, ok := last[fmt.Sprintf("%d:%s", n.ID, name)]
			wait := routeTraceEvery
			if ok && prev.Error != "" {
				wait = routeRetryAfter
			}
			if !ok || now.Sub(time.Unix(prev.CheckedAt, 0)) >= wait {
				due = true
				break
			}
		}
		if !due {
			continue
		}
		if targets == nil {
			if targets = s.resolveReturnTargets(ctx, cfg.Targets); len(targets) == 0 {
				return nil
			}
		}
		nodeCtx, cancel := context.WithTimeout(ctx, returnNodeTimeout)
		err := s.traceNode(nodeCtx, n, targets, last)
		cancel()
		if err != nil {
			s.log.Debug("return route trace failed", "node", n.Name, "error", err)
		}
	}
	return nil
}

// TraceNodeReturnRoutes traces one machine now, for the page's button.
func (s *Service) TraceNodeReturnRoutes(ctx context.Context, nodeID int64) error {
	if s.traceTool == nil {
		return errors.New("面板未配置路由追踪工具")
	}
	node, err := s.store.NodeByID(ctx, nodeID)
	if err != nil {
		return err
	}
	cfg, err := s.ReturnSettings(ctx)
	if err != nil {
		return err
	}
	targets := s.resolveReturnTargets(ctx, cfg.Targets)
	if len(targets) == 0 {
		return ErrReturnSettings
	}
	known, err := s.store.ReturnRoutes(ctx)
	if err != nil {
		return err
	}
	nodeCtx, cancel := context.WithTimeout(ctx, returnNodeTimeout)
	defer cancel()
	return s.traceNode(nodeCtx, node, targets, lastRoutes(known))
}

func (s *Service) traceNode(ctx context.Context, node *model.Node, targets []resolvedReturn, last map[string]store.ReturnRoute) error {
	now := time.Now().Unix()
	outputs, runErr := s.runTraces(ctx, node, targets)
	for i, t := range targets {
		rec := store.ReturnRoute{NodeID: node.ID, Target: t.Name, Verdict: "{}", Hops: "[]", CheckedAt: now}
		if runErr != nil {
			rec.Error = runErr.Error()
		} else if hops, err := parseNextTrace(outputs[i]); err != nil {
			rec.Error = err.Error()
		} else {
			routeclass.Annotate(hops)
			verdict := routeclass.Classify(hops, t.ip)
			rec.Line, rec.Confidence = verdict.Line, verdict.Confidence
			rawVerdict, _ := json.Marshal(verdict)
			rawHops, _ := json.Marshal(hops)
			rec.Verdict, rec.Hops = string(rawVerdict), string(rawHops)
		}
		if err := s.store.SetReturnRoute(ctx, rec); err != nil {
			return err
		}
		prev, had := last[fmt.Sprintf("%d:%s", node.ID, t.Name)]
		if had && lineChanged(prev, rec) {
			s.queueNotice(alertNotice{nodeID: node.ID, text: fmt.Sprintf("🔀 回程线路变化：%s → %s\n%s → %s", node.Name, t.Name, prev.Line, rec.Line)})
		}
	}
	return runErr
}

// lineChanged is a change of line both traces saw clearly, not a trace that
// saw less this time.
func lineChanged(prev, cur store.ReturnRoute) bool {
	if prev.Error != "" || cur.Error != "" || prev.Line == cur.Line {
		return false
	}
	return routeclass.Comparable(
		routeclass.Result{Line: prev.Line, Confidence: prev.Confidence},
		routeclass.Result{Line: cur.Line, Confidence: cur.Confidence})
}

// runTraces makes sure the pinned tracer is on the machine, then traces
// every target and returns each one's raw output.
func (s *Service) runTraces(ctx context.Context, node *model.Node, targets []resolvedReturn) ([]string, error) {
	want, ok := s.traceTool.SHA256(node.Arch)
	if !ok {
		return nil, fmt.Errorf("没有适用于 %q 架构的路由追踪工具", node.Arch)
	}
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return nil, err
	}
	bin := nextTraceDir + "/nexttrace-" + s.traceTool.Version()
	check, err := sshx.Run(ctx, client.Client, fmt.Sprintf("sha256sum %s 2>/dev/null | cut -d' ' -f1", bin))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(check.Stdout) != want {
		body, err := s.traceTool.Binary(ctx, node.Arch)
		if err != nil {
			return nil, fmt.Errorf("获取路由追踪工具失败：%w", err)
		}
		if _, err := sshx.Run(ctx, client.Client, "mkdir -p "+nextTraceDir); err != nil {
			return nil, err
		}
		if err := sshx.WriteFile(ctx, client.Client, bin, body, "0755"); err != nil {
			return nil, fmt.Errorf("上传路由追踪工具失败：%w", err)
		}
	}
	res, err := sshx.Run(ctx, client.Client, traceScript(bin, targets))
	if err != nil {
		return nil, err
	}
	return splitTraces(res.Stdout, len(targets)), nil
}

// traceScript traces each target twice, over ICMP and then TCP SYN to port
// 80, a few targets at a time, and prints both under a "== <index>" header.
// Routers rate-limit their replies, so each pass leaves different hops
// silent; together they see far more of the backbone than either alone.
//
// The ICMP pass asks NextTrace's IP database who and where each hop is, for
// the detail view; -M keeps it from publishing the route as a trace map.
// The line itself is judged from the panel's own prefix table, so a failed
// lookup only leaves a hop undescribed. The TCP pass skips the lookups.
func traceScript(bin string, targets []resolvedReturn) string {
	var b strings.Builder
	b.WriteString("unset NEXTTRACE_DATAPROVIDER\n")
	b.WriteString("d=$(mktemp -d) || exit 1\ntrap 'rm -rf \"$d\"' EXIT\n")
	fmt.Fprintf(&b, "t=; command -v timeout >/dev/null 2>&1 && t=\"timeout %d\"\n", int(returnTraceTimeout.Seconds()))
	for i, t := range targets {
		icmp := fmt.Sprintf("$t %s -q 3 -m 30 -M -j --traceroute %s", bin, t.ip)
		tcp := fmt.Sprintf("$t %s -T -p 80 -q 3 -m 30 -n -j -d disable-geoip --traceroute %s", bin, t.ip)
		fmt.Fprintf(&b, "{ %s; echo; %s; } > \"$d/%d\" 2>&1 &\n", icmp, tcp, i)
		if (i+1)%returnTraceParallel == 0 {
			b.WriteString("wait\n")
		}
	}
	b.WriteString("wait\n")
	for i := range targets {
		fmt.Fprintf(&b, "echo '== %d'; cat \"$d/%d\"; echo\n", i, i)
	}
	return b.String()
}

func splitTraces(out string, n int) []string {
	traces := make([]string, n)
	current := -1
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "== "); ok {
			if i, err := strconv.Atoi(strings.TrimSpace(rest)); err == nil && i >= 0 && i < n {
				current = i
				continue
			}
		}
		if current >= 0 {
			traces[current] += line + "\n"
		}
	}
	return traces
}

// nextTraceResult is the part of NextTrace's JSON the panel reads: for each
// TTL, every probe sent, answered or not. RTT is in nanoseconds.
type nextTraceResult struct {
	Hops [][]struct {
		Address *struct {
			IP string `json:"IP"`
		} `json:"Address"`
		Hostname string        `json:"Hostname"`
		RTT      int64         `json:"RTT"`
		Geo      *nextTraceGeo `json:"Geo"`
	} `json:"Hops"`
}

// nextTraceGeo is NextTrace's IP database answer for a hop; empty when the
// pass ran with geolocation off.
type nextTraceGeo struct {
	ASNumber string `json:"asnumber"`
	Country  string `json:"country"`
	Prov     string `json:"prov"`
	City     string `json:"city"`
	Owner    string `json:"owner"`
	ISP      string `json:"isp"`
	Whois    string `json:"whois"`
}

// describe copies what the tracer knows about a hop onto it, keeping what an
// earlier pass already filled in.
func (g *nextTraceGeo) describe(h *routeclass.Hop, hostname string) {
	if h.Hostname == "" {
		h.Hostname = strings.TrimSpace(hostname)
	}
	if g == nil {
		return
	}
	if h.ASN == 0 {
		h.ASN, _ = strconv.Atoi(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(g.ASNumber)), "AS"))
	}
	if h.Whois == "" {
		h.Whois = strings.TrimSpace(g.Whois)
	}
	if h.Location == "" {
		var parts []string
		for _, p := range []string{g.Country, g.Prov, g.City} {
			if p = strings.TrimSpace(p); p != "" && !slices.Contains(parts, p) {
				parts = append(parts, p)
			}
		}
		h.Location = strings.Join(parts, " ")
	}
	if h.Owner == "" {
		h.Owner = strings.Join(strings.Fields(g.Owner), " ")
		if h.Owner == "" {
			h.Owner = strings.TrimSpace(g.ISP)
		}
	}
}

// parseNextTrace reads every trace NextTrace printed for one target and
// merges them by TTL: a TTL is silent only if every pass found it silent,
// and one that answered from different routers keeps each of them.
func parseNextTrace(out string) ([]routeclass.Hop, error) {
	var runs []nextTraceResult
	for rest := out; ; {
		start := strings.Index(rest, "{")
		if start < 0 {
			break
		}
		dec := json.NewDecoder(strings.NewReader(rest[start:]))
		var res nextTraceResult
		if err := dec.Decode(&res); err != nil {
			rest = rest[start+1:]
			continue
		}
		runs = append(runs, res)
		rest = rest[start+int(dec.InputOffset()):]
	}
	if len(runs) == 0 {
		return nil, traceFailure(out)
	}

	depth := 0
	for _, r := range runs {
		depth = max(depth, len(r.Hops))
	}
	var hops []routeclass.Hop
	answered := false
	for ttl := 1; ttl <= depth; ttl++ {
		var seen []routeclass.Hop
		for _, r := range runs {
			if ttl > len(r.Hops) {
				continue
			}
			for _, p := range r.Hops[ttl-1] {
				if p.Address == nil || p.Address.IP == "" {
					continue
				}
				i := slices.IndexFunc(seen, func(h routeclass.Hop) bool { return h.IP == p.Address.IP })
				if i < 0 {
					seen = append(seen, routeclass.Hop{TTL: ttl, IP: p.Address.IP})
					i = len(seen) - 1
				}
				p.Geo.describe(&seen[i], p.Hostname)
				if p.RTT > 0 {
					ms := float64(p.RTT/10_000) / 100
					if seen[i].MS == nil || ms < *seen[i].MS {
						seen[i].MS = &ms
					}
				}
			}
		}
		if len(seen) == 0 {
			hops = append(hops, routeclass.Hop{TTL: ttl})
			continue
		}
		answered = true
		hops = append(hops, seen...)
	}
	if !answered {
		return nil, errors.New("追踪没有任何跳点回应（可能被防火墙拦截）")
	}
	return trimAfterDestination(hops), nil
}

// trimAfterDestination drops what follows the last answering hop: a pass that
// stopped early is followed by nothing, not by silence.
func trimAfterDestination(hops []routeclass.Hop) []routeclass.Hop {
	end := len(hops)
	for end > 0 && hops[end-1].IP == "" {
		end--
	}
	return hops[:end]
}

func traceFailure(out string) error {
	text := strings.TrimSpace(out)
	lower := strings.ToLower(text)
	switch {
	case text == "":
		return errors.New("路由追踪没有输出")
	case strings.Contains(lower, "operation not permitted") || strings.Contains(text, "root"):
		return errors.New("需要 root 权限才能发送追踪包")
	}
	if r := []rune(text); len(r) > 160 {
		text = string(r[:160]) + "…"
	}
	return fmt.Errorf("路由追踪失败：%s", text)
}

// ReturnOverview is the return-route part of the line-quality page.
type ReturnOverview struct {
	Settings   ReturnSettings    `json:"settings"`
	Presets    []ReturnTarget    `json:"presets"`
	Routes     []ReturnRouteView `json:"routes"`
	Tool       string            `json:"tool"`
	PrefixDate string            `json:"prefix_date"`
}

type ReturnRouteView struct {
	NodeID    int64             `json:"node_id"`
	Target    string            `json:"target"`
	Verdict   routeclass.Result `json:"verdict"`
	Hops      []routeclass.Hop  `json:"hops"`
	Error     string            `json:"error"`
	CheckedAt int64             `json:"checked_at"`
}

func (s *Service) returnOverview(ctx context.Context) (*ReturnOverview, error) {
	cfg, err := s.ReturnSettings(ctx)
	if err != nil {
		return nil, err
	}
	routes, err := s.store.ReturnRoutes(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]ReturnRouteView, 0, len(routes))
	for _, r := range routes {
		v := ReturnRouteView{NodeID: r.NodeID, Target: r.Target, Error: r.Error, CheckedAt: r.CheckedAt, Hops: []routeclass.Hop{}}
		if err := json.Unmarshal([]byte(r.Verdict), &v.Verdict); err != nil || v.Verdict.Chain == nil {
			v.Verdict.Chain = []string{}
		}
		if err := json.Unmarshal([]byte(r.Hops), &v.Hops); err != nil || v.Hops == nil {
			v.Hops = []routeclass.Hop{}
		}
		views = append(views, v)
	}
	tool := ""
	if s.traceTool != nil {
		tool = "NextTrace " + s.traceTool.Version()
	}
	return &ReturnOverview{Settings: cfg, Presets: ReturnPresets(), Routes: views, Tool: tool, PrefixDate: routeclass.PrefixDate()}, nil
}
