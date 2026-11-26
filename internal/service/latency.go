package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/sshx"
	"github.com/kukumi1/fluxlite/internal/store"
	"github.com/kukumi1/fluxlite/internal/verifier"
)

const (
	latencyTargetsKey = "latency.targets"
	// latencyProbes is how many connections each round opens to a test
	// point; loss is reported in steps of one in four.
	latencyProbes = 4
	// synRetryMS marks a connection whose first SYN was lost: the kernel
	// resends it after a second, so the connect still succeeds but its time
	// is that timer, not the path. Such a probe counts as a lost packet.
	synRetryMS = 1000
	// LatencyInterval is how often every machine measures its test points.
	LatencyInterval = 3 * time.Minute
	// latencyKeep bounds the history kept for trends.
	latencyKeep = 7 * 24 * time.Hour
	// latencyFreshFor is how old a sample may be and still describe a link.
	latencyFreshFor = 10 * time.Minute
	// latencyAlertHold is three rounds: one bad round is not an outage.
	latencyAlertHold  = 2*LatencyInterval + 30*time.Second
	maxLatencyTargets = 12
)

// LatencyTarget is a test point: an address inside a Chinese carrier's
// network, so the time from a machine to it is the time to that carrier's
// users in that region.
type LatencyTarget struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Enabled bool   `json:"enabled"`
}

var ErrLatencyTargets = errors.New("测试点设置无效")

// LatencyPresets are carrier test points in Beijing, Shanghai, Guangdong and
// Sichuan. The names resolve to the carriers' own addresses over IPv4 only;
// their IPv6 records point at a CDN abroad, which is why they are always
// resolved for IPv4 before being handed to a machine.
func LatencyPresets() []LatencyTarget {
	cities := [][2]string{{"北京", "bj"}, {"上海", "sh"}, {"广东", "gd"}, {"四川", "sc"}}
	carriers := [][2]string{{"电信", "ct"}, {"联通", "cu"}, {"移动", "cm"}}
	recommended := map[string]bool{"广东电信": true, "上海联通": true, "北京移动": true}
	var out []LatencyTarget
	for _, c := range cities {
		for _, k := range carriers {
			name := c[0] + k[0]
			out = append(out, LatencyTarget{
				Name: name, Host: fmt.Sprintf("%s-%s-v4.ip.zstaticcdn.com", c[1], k[1]),
				Port: 80, Enabled: recommended[name],
			})
		}
	}
	return out
}

// LatencyTargets returns the configured test points, or the presets with the
// three recommended ones switched on when none were saved yet.
func (s *Service) LatencyTargets(ctx context.Context) ([]LatencyTarget, error) {
	raw, ok, err := s.store.Setting(ctx, latencyTargetsKey)
	if err != nil {
		return nil, err
	}
	if !ok {
		return LatencyPresets(), nil
	}
	var targets []LatencyTarget
	if err := json.Unmarshal([]byte(raw), &targets); err != nil {
		return nil, err
	}
	return targets, nil
}

var hostPattern = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)

func (s *Service) SetLatencyTargets(ctx context.Context, targets []LatencyTarget) error {
	names := map[string]bool{}
	enabled := 0
	for i := range targets {
		t := &targets[i]
		t.Name, t.Host = strings.TrimSpace(t.Name), strings.TrimSpace(t.Host)
		if t.Name == "" || len([]rune(t.Name)) > 20 || names[t.Name] || !hostPattern.MatchString(t.Host) ||
			len(t.Host) > 253 || t.Port < 1 || t.Port > 65535 {
			return ErrLatencyTargets
		}
		names[t.Name] = true
		if t.Enabled {
			enabled++
		}
	}
	if len(targets) > 30 || enabled > maxLatencyTargets {
		return ErrLatencyTargets
	}
	raw, err := json.Marshal(targets)
	if err != nil {
		return err
	}
	return s.store.SetSetting(ctx, latencyTargetsKey, string(raw))
}

// resolvedTarget is a test point with the IPv4 address it resolved to.
type resolvedTarget struct {
	LatencyTarget
	ip string
}

func (s *Service) resolveTargets(ctx context.Context) ([]resolvedTarget, error) {
	targets, err := s.LatencyTargets(ctx)
	if err != nil {
		return nil, err
	}
	var out []resolvedTarget
	for _, t := range targets {
		if !t.Enabled {
			continue
		}
		ip, err := resolveIPv4(ctx, t.Host)
		if err != nil {
			s.log.Warn("resolve latency test point", "target", t.Name, "error", err)
			continue
		}
		out = append(out, resolvedTarget{LatencyTarget: t, ip: ip})
	}
	return out, nil
}

// resolveIPv4 resolves a test point on the panel, for IPv4 only: the
// carrier test points' IPv6 records lead to a CDN abroad.
func resolveIPv4(ctx context.Context, host string) (string, error) {
	if net.ParseIP(host) != nil {
		return host, nil
	}
	addrs, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
	if err != nil {
		return "", err
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("%s has no IPv4 address", host)
	}
	return addrs[0].String(), nil
}

// SampleLatency measures every online machine, a few at a time, and drops
// history older than a week.
func (s *Service) SampleLatency(ctx context.Context) error {
	targets, err := s.resolveTargets(ctx)
	if err != nil || len(targets) == 0 {
		return err
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for _, n := range nodes {
		if n.Status != model.StatusOnline {
			continue
		}
		wg.Add(1)
		slots <- struct{}{}
		go func(n *model.Node) {
			defer wg.Done()
			defer func() { <-slots }()
			nodeCtx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			if err := s.measureNode(nodeCtx, n, targets); err != nil {
				s.log.Debug("latency sample failed", "node", n.Name, "error", err)
			}
		}(n)
	}
	wg.Wait()
	return s.store.PruneLatency(ctx, time.Now().Add(-latencyKeep).Unix())
}

// MeasureNodeLatency measures one machine now, for the page's retest button.
func (s *Service) MeasureNodeLatency(ctx context.Context, nodeID int64) error {
	node, err := s.store.NodeByID(ctx, nodeID)
	if err != nil {
		return err
	}
	targets, err := s.resolveTargets(ctx)
	if err != nil {
		return err
	}
	return s.measureNode(ctx, node, targets)
}

func (s *Service) measureNode(ctx context.Context, node *model.Node, targets []resolvedTarget) error {
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return err
	}
	res, err := sshx.Run(ctx, client.Client, latencyScript(targets))
	if err != nil {
		return err
	}
	samples := parseLatency(res.Stdout, targets, node.ID, time.Now().Unix())
	return s.store.AddLatencySamples(ctx, samples)
}

// latencyScript probes every test point at once, each one latencyProbes
// times in a row, and prints "<index> <ok|fail|none> <ms|->" per probe.
func latencyScript(targets []resolvedTarget) string {
	var b strings.Builder
	for i, t := range targets {
		fmt.Fprintf(&b, "( for n in%s; do r=$(%s); echo \"%d $r\"; done ) &\n",
			strings.Repeat(" x", latencyProbes), verifier.TCPProbeCommand(t.ip, t.Port), i)
	}
	b.WriteString("wait\n")
	return b.String()
}

func parseLatency(out string, targets []resolvedTarget, nodeID, at int64) []store.LatencySample {
	type tally struct{ ok, done, timed, sum int }
	counts := make([]tally, len(targets))
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		i, err := strconv.Atoi(f[0])
		if err != nil || i < 0 || i >= len(targets) || f[1] == "none" {
			continue
		}
		c := &counts[i]
		c.done++
		if f[1] != "ok" {
			continue
		}
		ms, timed := 0, false
		if len(f) > 2 {
			if v, err := strconv.Atoi(f[2]); err == nil {
				ms, timed = v, true
			}
		}
		if timed && ms >= synRetryMS {
			continue
		}
		c.ok++
		if timed {
			c.timed++
			c.sum += ms
		}
	}
	var samples []store.LatencySample
	for i, c := range counts {
		if c.done == 0 {
			continue
		}
		sample := store.LatencySample{
			NodeID: nodeID, Target: targets[i].Name, At: at,
			Loss: (c.done - c.ok) * 100 / c.done,
		}
		if c.timed > 0 {
			avg := c.sum / c.timed
			sample.AvgMS = &avg
		}
		samples = append(samples, sample)
	}
	return samples
}

// latencyVerdict says why a sample is bad under the thresholds, or "".
func latencyVerdict(l store.LatencySample, cfg AlertSettings) string {
	switch {
	case l.Loss >= 100:
		return "连不上"
	case l.Loss > cfg.LossPercent:
		return fmt.Sprintf("丢包 %d%%（阈值 %d%%）", l.Loss, cfg.LossPercent)
	case cfg.LatencyMS > 0 && l.AvgMS != nil && *l.AvgMS > cfg.LatencyMS:
		return fmt.Sprintf("平均延迟 %dms（阈值 %dms）", *l.AvgMS, cfg.LatencyMS)
	}
	return ""
}

func (s *Service) latencySubjects(ctx context.Context, now time.Time, cfg AlertSettings) ([]alertSubject, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	latest, err := s.store.LatestLatency(ctx, now.Add(-latencyFreshFor).Unix())
	if err != nil {
		return nil, err
	}
	byNode := make(map[int64]map[string]store.LatencySample)
	for _, l := range latest {
		if byNode[l.NodeID] == nil {
			byNode[l.NodeID] = map[string]store.LatencySample{}
		}
		byNode[l.NodeID][l.Target] = l
	}
	targets, err := s.LatencyTargets(ctx)
	if err != nil {
		return nil, err
	}
	var subjects []alertSubject
	for _, n := range nodes {
		for _, t := range targets {
			if !t.Enabled {
				continue
			}
			sub := alertSubject{
				key: fmt.Sprintf("lat:%d:%s", n.ID, t.Name), kind: alertLatency, nodeID: n.ID,
				name: n.Name + " → " + t.Name, hold: latencyAlertHold,
			}
			if l, ok := byNode[n.ID][t.Name]; ok && n.Status == model.StatusOnline {
				if reason := latencyVerdict(l, cfg); reason != "" {
					sub.state, sub.reasons = obsFailing, []string{reason}
				} else {
					sub.state = obsOK
				}
			}
			subjects = append(subjects, sub)
		}
	}
	return subjects, nil
}

// badLatencyPoints names the links bad as of their latest sample, for the
// daily summary. Muted machines are left out like everywhere else.
func (s *Service) badLatencyPoints(ctx context.Context, now time.Time, cfg AlertSettings) ([]string, error) {
	subjects, err := s.latencySubjects(ctx, now, cfg)
	if err != nil {
		return nil, err
	}
	var bad []string
	for _, sub := range subjects {
		if sub.state == obsFailing && !cfg.muted(sub.nodeID) {
			bad = append(bad, sub.name)
		}
	}
	return bad, nil
}

// LatencyOverview is the latest picture for the line-quality page.
type LatencyOverview struct {
	Targets []LatencyTarget       `json:"targets"`
	Samples []store.LatencySample `json:"samples"`
	Presets []LatencyTarget       `json:"presets"`
	Return  *ReturnOverview       `json:"return"`
}

func (s *Service) LatencyOverview(ctx context.Context) (*LatencyOverview, error) {
	targets, err := s.LatencyTargets(ctx)
	if err != nil {
		return nil, err
	}
	samples, err := s.store.LatestLatency(ctx, time.Now().Add(-latencyFreshFor).Unix())
	if err != nil {
		return nil, err
	}
	if samples == nil {
		samples = []store.LatencySample{}
	}
	ret, err := s.returnOverview(ctx)
	if err != nil {
		return nil, err
	}
	return &LatencyOverview{Targets: targets, Samples: samples, Presets: LatencyPresets(), Return: ret}, nil
}

// LatencyHistory returns a machine's samples over the last hours, averaged
// into buckets so a chart gets a few hundred points at most.
func (s *Service) LatencyHistory(ctx context.Context, nodeID int64, hours int) ([]store.LatencySample, error) {
	if hours < 1 || hours > 24*7 {
		return nil, ErrLatencyTargets
	}
	bucket := int64(hours) * 3600 / 240
	if bucket < 180 {
		bucket = 180
	}
	out, err := s.store.LatencyHistory(ctx, nodeID, time.Now().Add(-time.Duration(hours)*time.Hour).Unix(), bucket)
	if out == nil {
		out = []store.LatencySample{}
	}
	return out, err
}
