package service

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/routeclass"
	"github.com/kukumi1/fluxlite/internal/sshx"
	"github.com/kukumi1/fluxlite/internal/store"
	"github.com/kukumi1/fluxlite/internal/verifier"

	"golang.org/x/crypto/ssh"
)

// What a test can include. The return-quality items probe each point's
// Zstatic node, which every province and carrier has; the speed test runs
// against the speed test servers.
const (
	ItemRoute = "route" // the line the route back takes, with TCP latency and loss
	ItemLarge = "large" // latency and loss of 1400-byte packets
	ItemUDP   = "udp"   // the line a UDP route back takes
	ItemSpeed = "speed"
)

var speedItems = []string{ItemRoute, ItemLarge, ItemUDP, ItemSpeed}

const (
	// qualityProbes is how many connections and large packets each point
	// gets; loss is reported in steps of five percent.
	qualityProbes   = 20
	largePacketSize = 1400
	// qualityParallel bounds the points a machine probes at once.
	qualityParallel = 4
	qualityTimeout  = 60 * time.Second
)

// provinceCodes name each province's Zstatic node.
var provinceCodes = map[string]string{
	"北京": "bj", "天津": "tj", "河北": "he", "山西": "sx", "内蒙古": "nm", "辽宁": "ln", "吉林": "jl", "黑龙江": "hl",
	"上海": "sh", "江苏": "js", "浙江": "zj", "安徽": "ah", "福建": "fj", "江西": "jx", "山东": "sd",
	"河南": "ha", "湖北": "hb", "湖南": "hn", "广东": "gd", "广西": "gx", "海南": "hi",
	"重庆": "cq", "四川": "sc", "贵州": "gz", "云南": "yn", "西藏": "xz",
	"陕西": "sn", "甘肃": "gs", "青海": "qh", "宁夏": "nx", "新疆": "xj",
}

var carrierCodes = map[string]string{"电信": "ct", "联通": "cu", "移动": "cm"}

// qualityHost is the Zstatic node for a point such as 四川移动.
func qualityHost(point string, family int) string {
	for c, code := range carrierCodes {
		if prov, ok := strings.CutSuffix(point, c); ok {
			return fmt.Sprintf("%s-%s-v%d.ip.zstaticcdn.com", provinceCodes[prov], code, family)
		}
	}
	return ""
}

func (r SpeedRequest) has(item string) bool { return slices.Contains(r.items(), item) }

// items are what the request asks for; a job saved before there was a
// choice was a speed test.
func (r SpeedRequest) items() []string {
	if len(r.Items) == 0 {
		return []string{ItemSpeed}
	}
	return r.Items
}

func (r SpeedRequest) quality() bool {
	return r.has(ItemRoute) || r.has(ItemLarge) || r.has(ItemUDP)
}

type qualityTarget struct {
	point string
	ip    string
	err   string
}

// qualityScript probes every point, a few at a time, and prints each
// item's raw output under "== <item> <index>".
func qualityScript(bin string, targets []qualityTarget, req SpeedRequest) string {
	var b strings.Builder
	b.WriteString("unset NEXTTRACE_DATAPROVIDER\n")
	b.WriteString("d=$(mktemp -d) || exit 1\ntrap 'rm -rf \"$d\"' EXIT\n")
	fmt.Fprintf(&b, "t=; command -v timeout >/dev/null 2>&1 && t=\"timeout %d\"\n", int(qualityTimeout.Seconds()))
	started := 0
	for i, t := range targets {
		if t.ip == "" {
			continue
		}
		var steps []string
		if req.has(ItemRoute) {
			steps = append(steps,
				fmt.Sprintf("for n in%s; do r=$(%s); echo \"$r\"; done > \"$d/lat.%d\" 2>&1", strings.Repeat(" x", qualityProbes), verifier.TCPProbeCommand(t.ip, 80), i),
				fmt.Sprintf("{ $t %s -q 3 -m 30 -M -j --traceroute %s; echo; $t %s -T -p 80 -q 3 -m 30 -n -j -d disable-geoip --traceroute %s; } > \"$d/route.%d\" 2>&1", bin, t.ip, bin, t.ip, i))
		}
		if req.has(ItemUDP) {
			steps = append(steps, fmt.Sprintf("$t %s -U -q 3 -m 30 -M -j --traceroute %s > \"$d/udp.%d\" 2>&1", bin, t.ip, i))
		}
		if req.has(ItemLarge) {
			// Every probe at the last TTL reaches the destination, so the
			// trace becomes a ping with packets of a set size.
			steps = append(steps, fmt.Sprintf("$t %s -T -p 80 --psize %d -f 30 -m 30 -q %d -n -j -d disable-geoip --traceroute %s > \"$d/large.%d\" 2>&1",
				bin, largePacketSize, qualityProbes, t.ip, i))
		}
		fmt.Fprintf(&b, "( %s ) &\n", strings.Join(steps, "; "))
		started++
		if started%qualityParallel == 0 {
			b.WriteString("wait\n")
		}
	}
	b.WriteString("wait\n")
	for i, t := range targets {
		if t.ip == "" {
			continue
		}
		for _, item := range []string{"lat", "route", "udp", "large"} {
			fmt.Fprintf(&b, "if [ -f \"$d/%s.%d\" ]; then echo '== %s %d'; cat \"$d/%s.%d\"; echo; fi\n", item, i, item, i, item, i)
		}
	}
	return b.String()
}

// splitQuality files the script's output by item and point.
func splitQuality(out string) map[string]string {
	sections := map[string]string{}
	current := ""
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "== "); ok {
			if f := strings.Fields(rest); len(f) == 2 {
				if _, err := strconv.Atoi(f[1]); err == nil {
					current = f[0] + " " + f[1]
					continue
				}
			}
		}
		if current != "" {
			sections[current] += line + "\n"
		}
	}
	return sections
}

// parseConnects reads the TCP probes: the median of the connections that
// did not need a second SYN, and the share that failed or needed one.
func parseConnects(out string) (ms *int, loss *int) {
	var times []int
	total, ok := 0, 0
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] == "none" || (f[0] != "ok" && f[0] != "fail") {
			continue
		}
		total++
		if f[0] != "ok" {
			continue
		}
		if len(f) > 1 {
			if v, err := strconv.Atoi(f[1]); err == nil {
				if v >= synRetryMS {
					continue
				}
				times = append(times, v)
			}
		}
		ok++
	}
	if total == 0 {
		return nil, nil
	}
	l := (total - ok) * 100 / total
	loss = &l
	if len(times) > 0 {
		m := median(times)
		ms = &m
	}
	return ms, loss
}

// parseLarge reads the large-packet trace: every probe the destination
// answered, out of all sent.
func parseLarge(out, ip string) (ms *int, loss *int, err error) {
	start := strings.Index(out, "{")
	if start < 0 {
		return nil, nil, traceFailure(out)
	}
	var res nextTraceResult
	if err := json.NewDecoder(strings.NewReader(out[start:])).Decode(&res); err != nil {
		return nil, nil, traceFailure(out)
	}
	var times []int
	for _, hop := range res.Hops {
		for _, p := range hop {
			if p.Address != nil && p.Address.IP == ip && p.RTT > 0 {
				times = append(times, int(p.RTT/1_000_000))
			}
		}
	}
	l := max(0, (qualityProbes-len(times))*100/qualityProbes)
	loss = &l
	if len(times) > 0 {
		m := median(times)
		ms = &m
	}
	return ms, loss, nil
}

// routeLine classifies a traced route back by the carrier network it enters.
func routeLine(out, ip string) (string, error) {
	hops, err := parseNextTrace(out)
	if err != nil {
		return "", err
	}
	routeclass.Annotate(hops)
	v := routeclass.Classify(hops, ip)
	if v.Line == "" {
		return "", fmt.Errorf("看不出线路（%s）", v.Evidence)
	}
	line := v.Line
	if v.Handoff != "" {
		line += "→" + v.Handoff
	}
	return line, nil
}

// qualityNode runs the return-quality items on one machine and records a
// result for every point and item.
func (s *Service) qualityNode(ctx context.Context, client *ssh.Client, id int64, req SpeedRequest, node *model.Node, family int) error {
	if s.traceTool == nil {
		return fmt.Errorf("面板未配置路由追踪工具")
	}
	targets := make([]qualityTarget, len(req.Points))
	for i, p := range req.Points {
		targets[i].point = p
		ip, err := resolveIP(ctx, qualityHost(p, family), family)
		if err != nil {
			targets[i].err = fmt.Sprintf("%s 没有 IPv%d 节点", p, family)
			continue
		}
		targets[i].ip = ip
	}
	bin, err := ensureTool(ctx, client, node, s.traceTool, "nexttrace", "路由追踪工具")
	if err != nil {
		return err
	}
	res, err := sshx.Run(ctx, client, qualityScript(bin, targets, req))
	if err != nil {
		return fmt.Errorf("回程检测失败：%w", err)
	}
	sections := splitQuality(res.Stdout)
	for i, t := range targets {
		for _, item := range []string{ItemRoute, ItemUDP, ItemLarge} {
			if !req.has(item) {
				continue
			}
			r := store.QualityResult{JobID: id, NodeID: node.ID, Point: t.point, Item: item, Family: family}
			out := sections[fmt.Sprintf("%s %d", item, i)]
			switch {
			case t.err != "":
				r.Error = t.err
			case item == ItemLarge:
				var perr error
				r.MS, r.Loss, perr = parseLarge(out, t.ip)
				if perr != nil {
					r.Error = perr.Error()
				}
			default:
				line, lerr := routeLine(out, t.ip)
				if lerr != nil {
					r.Error = lerr.Error()
				}
				r.Line = line
				if item == ItemRoute {
					r.MS, r.Loss = parseConnects(sections[fmt.Sprintf("lat %d", i)])
				}
			}
			if err := s.store.AddQualityResult(context.Background(), r); err != nil {
				s.log.Error("record quality result", "job", id, "error", err)
			}
		}
	}
	return nil
}
