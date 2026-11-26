package service

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/sshx"
	"github.com/kukumi1/fluxlite/internal/verifier"
)

// BlockKind is what a machine's reach into China says about how it is
// blocked. The wall drops every packet to and from a blocked address, so a
// machine that can still ping China but not open a TCP connection there has
// lost TCP only, and one that can do neither has lost the address.
type BlockKind string

const (
	BlockNone       BlockKind = "ok"
	BlockPartial    BlockKind = "partial"
	BlockTCP        BlockKind = "tcp_blocked"
	BlockIP         BlockKind = "ip_blocked"
	BlockTCPUnknown BlockKind = "tcp_down"
	BlockNoTool     BlockKind = "no_tool"
)

const blockProbes = 4

// BlockTarget is one test point's result. ICMPLoss is nil where the machine
// has no ping to measure with.
type BlockTarget struct {
	Name     string `json:"name"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	ICMPLoss *int   `json:"icmp_loss"`
	TCPOK    int    `json:"tcp_ok"`
	TCPTotal int    `json:"tcp_total"`
}

type BlockFamily struct {
	Family  int           `json:"family"`
	Kind    BlockKind     `json:"kind"`
	Label   string        `json:"label"`
	Note    string        `json:"note,omitempty"`
	Targets []BlockTarget `json:"targets"`
}

type BlockResult struct {
	NodeID   int64         `json:"node_id"`
	Name     string        `json:"name"`
	Error    string        `json:"error,omitempty"`
	Families []BlockFamily `json:"families"`
}

// blockFallback is measured for a family no latency point is switched on
// for, so every machine is checked over both whatever the page shows.
var blockFallback = []string{"广东电信", "上海联通", "北京移动"}

func (s *Service) blockTargets(ctx context.Context) ([]resolvedTarget, error) {
	targets, err := s.resolveTargets(ctx)
	if err != nil {
		return nil, err
	}
	for _, family := range []int{4, 6} {
		if slices.ContainsFunc(targets, func(t resolvedTarget) bool { return t.family == family }) {
			continue
		}
		var fallback []LatencyTarget
		for _, p := range LatencyPresets() {
			if slices.Contains(blockFallback, p.Name) {
				p.Enabled, p.EnabledV6 = family == 4, family == 6
				fallback = append(fallback, p)
			}
		}
		targets = append(targets, s.resolveList(ctx, fallback)...)
	}
	return targets, nil
}

// BlockCheck pings and connects from every machine to the carrier test
// points and says, per address family, whether China is reachable and if not
// how it is blocked. A machine the panel reaches over SSH is reachable from
// abroad, which is what makes a failure here a block rather than an outage.
func (s *Service) BlockCheck(ctx context.Context) ([]BlockResult, error) {
	targets, err := s.blockTargets(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]BlockResult, len(nodes))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 6)
	for i, n := range nodes {
		results[i] = BlockResult{NodeID: n.ID, Name: n.Name, Families: []BlockFamily{}}
		if n.Status != model.StatusOnline {
			results[i].Error = "机器离线，未检测"
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func(r *BlockResult, n *model.Node) {
			defer wg.Done()
			defer func() { <-slots }()
			nodeCtx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			mine := forNode(targets, n)
			client, err := s.pool.Get(nodeCtx, n)
			if err != nil {
				r.Error = "连不上机器：" + err.Error()
				return
			}
			res, err := sshx.Run(nodeCtx, client.Client, blockScript(mine))
			if err != nil {
				r.Error = "检测失败：" + err.Error()
				return
			}
			r.Families = judgeBlock(parseBlock(res.Stdout, mine))
		}(&results[i], n)
	}
	wg.Wait()
	return results, nil
}

// blockScript pings and connects to every point at once and prints
// "<index> icmp <loss%|none>" and "<index> tcp <ok|fail|none> <ms|->".
func blockScript(targets []resolvedTarget) string {
	var b strings.Builder
	for i, t := range targets {
		ping := "ping -n -q -c 4 -w 8 " + t.ip
		if t.family == 6 {
			ping = "ping -6 -n -q -c 4 -w 8 " + t.ip
		}
		loss := ` 2>/dev/null | grep -o '[0-9.]*% packet loss' | cut -d% -f1)`
		fmt.Fprintf(&b, "( if command -v ping >/dev/null 2>&1; then l=$(%s%s; ", ping, loss)
		if t.family == 6 {
			fmt.Fprintf(&b, "[ -n \"$l\" ] || l=$(ping6 -n -q -c 4 -w 8 %s%s; ", t.ip, loss)
		}
		fmt.Fprintf(&b, "echo \"%d icmp ${l:-none}\"; else echo \"%d icmp none\"; fi ) &\n", i, i)
		fmt.Fprintf(&b, "( for n in%s; do r=$(%s); echo \"%d tcp $r\"; done ) &\n",
			strings.Repeat(" x", blockProbes), verifier.TCPProbeCommand(t.ip, t.Port), i)
	}
	b.WriteString("wait\n")
	return b.String()
}

type blockReading struct {
	target resolvedTarget
	BlockTarget
}

func parseBlock(out string, targets []resolvedTarget) []blockReading {
	readings := make([]blockReading, len(targets))
	for i, t := range targets {
		readings[i] = blockReading{target: t, BlockTarget: BlockTarget{Name: t.Name, IP: t.ip, Port: t.Port}}
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		i, err := strconv.Atoi(f[0])
		if err != nil || i < 0 || i >= len(readings) {
			continue
		}
		r := &readings[i]
		switch f[1] {
		case "icmp":
			if v, err := strconv.ParseFloat(f[2], 64); err == nil {
				loss := int(v + 0.5)
				r.ICMPLoss = &loss
			}
		case "tcp":
			if f[2] == "none" {
				continue
			}
			r.TCPTotal++
			if f[2] == "ok" {
				r.TCPOK++
			}
		}
	}
	return readings
}

func judgeBlock(readings []blockReading) []BlockFamily {
	var out []BlockFamily
	for _, family := range []int{4, 6} {
		var targets []BlockTarget
		for _, r := range readings {
			if r.target.family == family {
				targets = append(targets, r.BlockTarget)
			}
		}
		if len(targets) > 0 {
			out = append(out, judgeFamily(family, targets))
		}
	}
	return out
}

func judgeFamily(family int, targets []BlockTarget) BlockFamily {
	f := BlockFamily{Family: family, Targets: targets}
	var dead []string
	tested, pingKnown, pingReached := 0, false, false
	for _, t := range targets {
		if t.ICMPLoss != nil {
			pingKnown = true
			pingReached = pingReached || *t.ICMPLoss < 100
		}
		if t.TCPTotal == 0 {
			continue
		}
		tested++
		if t.TCPOK == 0 {
			dead = append(dead, t.Name)
		}
	}
	switch {
	case tested == 0:
		f.Kind, f.Label = BlockNoTool, "无法检测"
		f.Note = "机器上没有可用的 TCP 探测工具（python3、bash 或 nc）"
	case len(dead) == 0:
		f.Kind, f.Label = BlockNone, "正常"
		if pingKnown && !pingReached {
			f.Note = "TCP 都能连上；测试点不回应 ping，不影响结论"
		}
	case len(dead) < tested:
		f.Kind, f.Label = BlockPartial, "部分线路不通"
		f.Note = strings.Join(dead, "、") + " 连不上，其余正常；多半是线路问题，不是被封"
	case pingReached:
		f.Kind, f.Label = BlockTCP, "TCP 阻断"
		f.Note = "ping 得通国内，TCP 一个都连不上：地址没被封，TCP 被阻断"
	case pingKnown:
		f.Kind, f.Label = BlockIP, "疑似 IP 被墙"
		f.Note = "ping 和 TCP 都到不了国内，而境外能正常连上这台机器"
	default:
		f.Kind, f.Label = BlockTCPUnknown, "国内 TCP 不通"
		f.Note = "机器上没有 ping，分不出是 IP 被墙还是 TCP 阻断"
	}
	return f
}
