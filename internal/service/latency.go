package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/store"
)

const (
	latencyTargetsKey = "latency.targets"
	// synRetryMS marks a connection whose first SYN was lost: the kernel
	// resends it after a second, so the connect still succeeds but its time
	// is that timer, not the path. Such a probe counts as a lost packet.
	synRetryMS        = 1000
	maxLatencyTargets = 12
)

// LatencyTarget is a test point: an address inside a Chinese carrier's
// network, so the time from a machine to it is the time to that carrier's
// users in that region. Host is where it answers over IPv4 and HostV6 over
// IPv6; a point may have either or both. Enabled and EnabledV6 choose,
// independently, whether it is measured over each, which needs that host.
type LatencyTarget struct {
	Name      string `json:"name"`
	Host      string `json:"host,omitempty"`
	HostV6    string `json:"host_v6,omitempty"`
	Port      int    `json:"port"`
	Enabled   bool   `json:"enabled"`
	EnabledV6 bool   `json:"enabled_v6"`
}

var ErrLatencyTargets = errors.New("测试点设置无效")

// LatencyPresets are carrier test points. Beijing, Shanghai and Guangdong
// come as two names each: the v4 one resolves to the carrier's own address
// over IPv4 only (its IPv6 record leads to a CDN abroad), the v6 one to the
// carrier's IPv6 address. Chengdu is IPv4 only, on nodes that answer on 443;
// Sichuan is IPv6 only. The panel resolves every name for its own family
// before handing the address to a machine.
func LatencyPresets() []LatencyTarget {
	carriers := [][2]string{{"电信", "ct"}, {"联通", "cu"}, {"移动", "cm"}}
	var out []LatencyTarget
	for _, c := range [][2]string{{"北京", "bj"}, {"上海", "sh"}, {"广东", "gd"}} {
		for _, k := range carriers {
			out = append(out, LatencyTarget{
				Name: c[0] + k[0], Host: fmt.Sprintf("%s-%s-v4.ip.zstaticcdn.com", c[1], k[1]),
				HostV6: fmt.Sprintf("%s-%s-v6.ip.zstaticcdn.com", c[1], k[1]), Port: 80,
			})
		}
	}
	// Chengdu over IPv4 and Sichuan over IPv6 are on until chosen otherwise.
	out = append(out,
		LatencyTarget{Name: "成都电信", Host: "118.123.218.132", Port: 443, Enabled: true},
		LatencyTarget{Name: "成都联通", Host: "101.207.252.191", Port: 443, Enabled: true},
		LatencyTarget{Name: "成都移动", Host: "sc-chengdu-cm-v4.ip.zstaticcdn.com", Port: 443, Enabled: true},
	)
	for _, k := range carriers {
		out = append(out, LatencyTarget{Name: "四川" + k[0], HostV6: fmt.Sprintf("sc-%s-v6.ip.zstaticcdn.com", k[1]), Port: 80, EnabledV6: true})
	}
	return out
}

// LatencyTargets returns the configured test points, or the presets with the
// three recommended ones switched on when none were saved yet. A saved point
// named like a preset takes the preset's addresses; only its switches are
// the operator's.
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
	var fields []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, err
	}
	presets := map[string]LatencyTarget{}
	for _, p := range LatencyPresets() {
		presets[p.Name] = p
	}
	var chengdu []string
	for i := range targets {
		t := &targets[i]
		// Points saved before IPv6 was measured carry no v6 switch; they are
		// measured over IPv6 wherever they were over IPv4.
		if _, chosen := fields[i]["enabled_v6"]; !chosen {
			t.EnabledV6 = t.Enabled
		}
		p, preset := presets[t.Name]
		if !preset {
			t.EnabledV6 = t.EnabledV6 && t.HostV6 != ""
			continue
		}
		// Sichuan used to be measured over IPv4 too; that is Chengdu's now.
		if strings.HasPrefix(t.Name, "四川") && t.Enabled && p.Host == "" {
			chengdu = append(chengdu, "成都"+strings.TrimPrefix(t.Name, "四川"))
		}
		t.Host, t.HostV6, t.Port = p.Host, p.HostV6, p.Port
		t.Enabled = t.Enabled && t.Host != ""
		t.EnabledV6 = t.EnabledV6 && t.HostV6 != ""
	}
	for _, name := range chengdu {
		if i := slices.IndexFunc(targets, func(t LatencyTarget) bool { return t.Name == name }); i >= 0 {
			targets[i].Enabled = true
		} else {
			p := presets[name]
			p.Enabled = true
			targets = append(targets, p)
		}
	}
	return targets, nil
}

var hostPattern = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)

// validHostV4 accepts a name or an IPv4 literal.
func validHostV4(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.To4() != nil
	}
	return hostPattern.MatchString(host) && len(host) <= 253
}

// validHostV6 accepts a name or an IPv6 literal, never an IPv4 one: a v6
// point that is really an IPv4 address would measure IPv4 twice.
func validHostV6(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.To4() == nil
	}
	return hostPattern.MatchString(host) && len(host) <= 253
}

func (s *Service) SetLatencyTargets(ctx context.Context, targets []LatencyTarget) error {
	names := map[string]bool{}
	enabled, enabledV6 := 0, 0
	for i := range targets {
		t := &targets[i]
		t.Name, t.Host, t.HostV6 = strings.TrimSpace(t.Name), strings.TrimSpace(t.Host), strings.TrimSpace(t.HostV6)
		if t.Name == "" || len([]rune(t.Name)) > 20 || names[t.Name] || (t.Host == "" && t.HostV6 == "") ||
			(t.Host != "" && !validHostV4(t.Host)) || (t.HostV6 != "" && !validHostV6(t.HostV6)) ||
			(t.Enabled && t.Host == "") || (t.EnabledV6 && t.HostV6 == "") || t.Port < 1 || t.Port > 65535 {
			return ErrLatencyTargets
		}
		names[t.Name] = true
		if t.Enabled {
			enabled++
		}
		if t.EnabledV6 {
			enabledV6++
		}
	}
	if len(targets) > 30 || enabled > maxLatencyTargets || enabledV6 > maxLatencyTargets {
		return ErrLatencyTargets
	}
	raw, err := json.Marshal(targets)
	if err != nil {
		return err
	}
	return s.store.SetSetting(ctx, latencyTargetsKey, string(raw))
}

// resolvedTarget is a test point with the address it resolved to for one
// address family.
type resolvedTarget struct {
	LatencyTarget
	family int
	ip     string
}

// resolveTargets resolves every test point for each address family it is
// switched on for.
func (s *Service) resolveTargets(ctx context.Context) ([]resolvedTarget, error) {
	targets, err := s.LatencyTargets(ctx)
	if err != nil {
		return nil, err
	}
	return s.resolveList(ctx, targets), nil
}

// resolveList resolves the given points for each family they are switched
// on for, leaving out any that do not resolve.
func (s *Service) resolveList(ctx context.Context, targets []LatencyTarget) []resolvedTarget {
	var out []resolvedTarget
	for _, t := range targets {
		for _, family := range []int{4, 6} {
			host := t.Host
			if family == 6 {
				host = t.HostV6
			}
			if on := map[int]bool{4: t.Enabled, 6: t.EnabledV6}[family]; !on || host == "" {
				continue
			}
			ip, err := resolveIP(ctx, host, family)
			if err != nil {
				s.log.Warn("resolve latency test point", "target", t.Name, "family", family, "error", err)
				continue
			}
			out = append(out, resolvedTarget{LatencyTarget: t, family: family, ip: ip})
		}
	}
	return out
}

// forNode drops the IPv6 points for a machine with no IPv6 of its own: every
// probe would fail, and that is no news about the line.
func forNode(targets []resolvedTarget, node *model.Node) []resolvedTarget {
	if node.IPv6Address != "" {
		return targets
	}
	var out []resolvedTarget
	for _, t := range targets {
		if t.family == 4 {
			out = append(out, t)
		}
	}
	return out
}

// resolveIP resolves a test point on the panel for one address family.
func resolveIP(ctx context.Context, host string, family int) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		if (ip.To4() != nil) != (family == 4) {
			return "", fmt.Errorf("%s is not an IPv%d address", host, family)
		}
		return host, nil
	}
	network := "ip4"
	if family == 6 {
		network = "ip6"
	}
	addrs, err := net.DefaultResolver.LookupIP(ctx, network, host)
	if err != nil {
		return "", err
	}
	if len(addrs) == 0 {
		return "", fmt.Errorf("%s has no IPv%d address", host, family)
	}
	return addrs[0].String(), nil
}

// median is a run of readings' usual value: one slow reading among them is a
// hiccup, not the path, and the mean would carry it into the whole run.
func median(times []int) int {
	slices.Sort(times)
	mid := len(times) / 2
	if len(times)%2 == 0 {
		return (times[mid-1] + times[mid]) / 2
	}
	return times[mid]
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
	links, err := s.probeLinks(ctx, now)
	if err != nil {
		return nil, err
	}
	online := map[int64]*model.Node{}
	for _, n := range nodes {
		if n.Status == model.StatusOnline {
			online[n.ID] = n
		}
	}
	subjects := make([]alertSubject, 0, len(links))
	for _, l := range links {
		n, ok := online[l.NodeID]
		if !ok {
			continue
		}
		sub := alertSubject{
			key: fmt.Sprintf("lat:%d:%s", n.ID, l.Target), kind: alertLatency, nodeID: n.ID,
			name: n.Name + " → " + l.Target, hold: latencyAlertHold, state: obsOK,
		}
		if reason := latencyVerdict(l, cfg); reason != "" {
			sub.state, sub.reasons = obsFailing, []string{reason}
		}
		subjects = append(subjects, sub)
	}
	return subjects, nil
}

// badLatencyPoints names the links bad as of their latest sample, for the
// daily summary. Machines left out of latency messages are left out here too.
func (s *Service) badLatencyPoints(ctx context.Context, now time.Time, cfg AlertSettings) ([]string, error) {
	subjects, err := s.latencySubjects(ctx, now, cfg)
	if err != nil {
		return nil, err
	}
	var bad []string
	for _, sub := range subjects {
		if sub.state == obsFailing && !cfg.excluded(EventLatency, sub.nodeID) {
			bad = append(bad, sub.name)
		}
	}
	return bad, nil
}

// LatencyOverview is the latest picture for the line-quality page.
type LatencyOverview struct {
	Targets []LatencyTarget `json:"targets"`
	Presets []LatencyTarget `json:"presets"`
	Return  *ReturnOverview `json:"return"`
	Blocks  []BlockState    `json:"blocks"`
}

func (s *Service) LatencyOverview(ctx context.Context) (*LatencyOverview, error) {
	targets, err := s.LatencyTargets(ctx)
	if err != nil {
		return nil, err
	}
	ret, err := s.returnOverview(ctx)
	if err != nil {
		return nil, err
	}
	blocks, err := s.blockStates(ctx)
	if err != nil {
		return nil, err
	}
	return &LatencyOverview{Targets: targets, Presets: LatencyPresets(), Return: ret, Blocks: blocks}, nil
}
