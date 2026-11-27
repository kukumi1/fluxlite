package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

// routeAlertHold is how long a route has to stay failed, or stay recovered,
// before it is reported. Route samples come every ten seconds, and a single
// dial that times out under load is not an outage worth waking someone for.
const routeAlertHold = 30 * time.Second

// sampleFreshFor bounds how old a hop's sample may be and still describe the
// hop. Older than this the sampler has stopped reaching it, and the reading
// says nothing about now.
const sampleFreshFor = 2 * time.Minute

type observation int

const (
	// obsUnknown leaves whatever was last reported standing. It covers a route
	// through a machine that is offline — the machine's own alert already
	// says so — and samples too old to judge by.
	obsUnknown observation = iota
	obsOK
	obsFailing
)

type alertKind int

const (
	alertNode alertKind = iota
	alertRoute
	alertLatency
	alertSpike
)

// alertSubject is one machine or route as it looks right now.
type alertSubject struct {
	key     string
	kind    alertKind
	nodeID  int64 // machine the subject is about, for muting; zero for routes
	name    string
	state   observation
	hold    time.Duration
	reasons []string   // route: why it is failing
	routes  []string   // machine: enabled routes through it
	seen    *time.Time // machine: when it last answered
}

type alertEntry struct {
	failing bool
	// since is when the reported state began; for a failure, when it was
	// first observed rather than when it was reported.
	since time.Time
	// pending is when the observed state started to differ from the reported
	// one, zero while they agree.
	pending time.Time
}

type alertEvent struct {
	subject alertSubject
	failing bool
	lasted  time.Duration
}

// alertTracker remembers what has been reported so that only changes are.
// It lives in memory: after a restart the first pass records the state as it
// stands without reporting it, so a restart never replays known outages.
type alertTracker struct {
	mu      sync.Mutex
	primed  bool
	entries map[string]*alertEntry
}

func (t *alertTracker) observe(now time.Time, subjects []alertSubject) []alertEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = make(map[string]*alertEntry)
	}

	var events []alertEvent
	present := make(map[string]bool, len(subjects))
	for _, sub := range subjects {
		present[sub.key] = true
		e, known := t.entries[sub.key]
		if !known {
			e = &alertEntry{since: now, failing: !t.primed && sub.state == obsFailing}
			t.entries[sub.key] = e
			if !t.primed {
				continue
			}
		}
		if sub.state == obsUnknown {
			e.pending = time.Time{}
			continue
		}
		failing := sub.state == obsFailing
		if failing == e.failing {
			e.pending = time.Time{}
			continue
		}
		if e.pending.IsZero() {
			e.pending = now
		}
		if now.Sub(e.pending) < sub.hold {
			continue
		}
		ev := alertEvent{subject: sub, failing: failing}
		if !failing {
			// The outage ended when recovery was first seen, not once it had
			// been held long enough to report.
			ev.lasted = e.pending.Sub(e.since)
		}
		e.failing, e.since, e.pending = failing, e.pending, time.Time{}
		events = append(events, ev)
	}
	// A route stopped or deleted, or a machine removed, is gone by the
	// operator's hand; reporting it as recovered would be noise.
	for key := range t.entries {
		if !present[key] {
			delete(t.entries, key)
		}
	}
	t.primed = true
	return events
}

// AlertDigest reports, as one message ready to send, what changed since the
// last call: machines going offline or coming back, routes failing or
// recovering, allowances running low, nodes about to expire, test points
// turning bad, return routes switching lines, and the daily summary when it
// is due. It returns "" when there is nothing to say.
//
// Every tracker advances whether or not its kind of message is switched on,
// so switching one on later does not replay what happened in between.
func (s *Service) AlertDigest(ctx context.Context) (string, error) {
	now := time.Now()
	cfg, err := s.AlertSettings(ctx)
	if err != nil {
		return "", err
	}
	subjects, err := s.alertSubjects(ctx, now)
	if err != nil {
		return "", err
	}
	latency, err := s.latencySubjects(ctx, now, cfg)
	if err != nil {
		return "", err
	}
	levels, err := s.levelSubjects(ctx, now)
	if err != nil {
		return "", err
	}
	spikes, err := s.spikeSubjects(ctx, now)
	if err != nil {
		return "", err
	}
	latency = append(latency, spikes...)

	var parts []string
	for _, ev := range s.alerts.observe(now, append(subjects, latency...)) {
		if cfg.allows(ev.subject) {
			parts = append(parts, alertEventText(ev))
		}
	}
	due, err := s.machinesDue(ctx)
	if err != nil {
		return "", err
	}
	levels = append(levels, machineExpiryLevels(due, now, cfg.MachineExpiryDays)...)
	for _, ev := range s.levels.observe(levels) {
		switch {
		case ev.kind == levelQuota && cfg.Events.Quota, ev.kind == levelExpiry && cfg.Events.Expiry:
			parts = append(parts, ev.text)
		case ev.kind == levelMachineExpiry && cfg.Events.MachineExpiry && !cfg.excluded(EventMachineDue, ev.nodeID):
			parts = append(parts, ev.text)
		}
	}
	for _, notice := range s.drainNotices() {
		if cfg.allowsNotice(notice) {
			parts = append(parts, notice.text)
		}
	}
	if cfg.Events.Daily {
		summary, err := s.dailySummary(ctx, now, cfg)
		if err != nil {
			s.log.Warn("daily summary", "error", err)
		}
		if summary != "" {
			parts = append(parts, summary)
		}
	}
	return strings.Join(parts, "\n\n"), nil
}

// allowsNotice checks a notice against its event's switch and machines.
func (a AlertSettings) allowsNotice(n alertNotice) bool {
	if n.kind == noticeBlocked {
		return a.Events.Blocked && !a.excluded(EventBlocked, n.nodeID)
	}
	return a.Events.RouteChange && !a.excluded(EventRouteChange, n.nodeID)
}

func (a AlertSettings) allows(sub alertSubject) bool {
	switch sub.kind {
	case alertNode:
		return a.Events.NodeOffline && !a.excluded(EventNodeOffline, sub.nodeID)
	case alertRoute:
		return a.Events.RouteDown
	case alertLatency:
		return a.Events.Latency && !a.excluded(EventLatency, sub.nodeID)
	case alertSpike:
		return a.Events.LatencySpike && !a.excluded(EventLatencySpike, sub.nodeID)
	}
	return false
}

func (s *Service) alertSubjects(ctx context.Context, now time.Time) ([]alertSubject, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}

	byID := make(map[int64]*model.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	through := make(map[int64][]string)
	var subjects []alertSubject
	for _, r := range routes {
		if !r.Enabled {
			continue
		}
		subjects = append(subjects, routeSubject(r, byID, now))
		listed := make(map[int64]bool)
		for _, hop := range r.Hops {
			if !listed[hop.NodeID] {
				listed[hop.NodeID] = true
				through[hop.NodeID] = append(through[hop.NodeID], r.Name)
			}
		}
	}
	for _, n := range nodes {
		state := obsUnknown
		switch n.Status {
		case model.StatusOnline:
			state = obsOK
		case model.StatusOffline:
			state = obsFailing
		}
		subjects = append(subjects, alertSubject{
			key: fmt.Sprintf("node:%d", n.ID), kind: alertNode, nodeID: n.ID, name: n.Name,
			state: state, routes: through[n.ID], seen: n.LastSeen,
		})
	}
	return subjects, nil
}

// routeSubject judges a route from its hops' latest samples. It fails when a
// relay is not running, or when a hop cannot open a connection to its next
// hop or landing. It is unknown, rather than failing, while a machine on it is
// offline or its samples have gone stale.
func routeSubject(r *model.Route, nodes map[int64]*model.Node, now time.Time) alertSubject {
	sub := alertSubject{key: fmt.Sprintf("route:%d", r.ID), kind: alertRoute, name: r.Name, hold: routeAlertHold}
	fresh := func(at *time.Time) bool { return at != nil && now.Sub(*at) <= sampleFreshFor }
	nameOf := func(id int64) string {
		if n := nodes[id]; n != nil {
			return n.Name
		}
		return fmt.Sprintf("#%d", id)
	}

	known := true
	for i, hop := range r.Hops {
		if n := nodes[hop.NodeID]; n == nil || n.Status != model.StatusOnline {
			return sub
		}
		if hop.Running == nil || !fresh(hop.CheckedAt) {
			known = false
			continue
		}
		if !*hop.Running {
			sub.reasons = append(sub.reasons, nameOf(hop.NodeID)+" 上的转发进程没在运行")
		}
		if hop.Reachable == nil || *hop.Reachable || !fresh(hop.ReachAt) {
			continue
		}
		if i == len(r.Hops)-1 {
			sub.reasons = append(sub.reasons, fmt.Sprintf("%s 连不上落地 %s", nameOf(hop.NodeID), r.Target))
			continue
		}
		// A next hop whose own relay is down cannot be reached either; that
		// is already said, once, about the hop itself.
		next := r.Hops[i+1]
		if next.Running != nil && !*next.Running {
			continue
		}
		sub.reasons = append(sub.reasons, fmt.Sprintf("%s 连不上下一跳 %s", nameOf(hop.NodeID), nameOf(next.NodeID)))
	}
	switch {
	case len(sub.reasons) > 0:
		sub.state = obsFailing
	case known:
		sub.state = obsOK
	}
	return sub
}

func alertMessage(events []alertEvent) string {
	parts := make([]string, 0, len(events))
	for _, ev := range events {
		parts = append(parts, alertEventText(ev))
	}
	return strings.Join(parts, "\n\n")
}

func alertEventText(ev alertEvent) string {
	var b strings.Builder
	sub := ev.subject
	switch {
	case sub.kind == alertNode && ev.failing:
		fmt.Fprintf(&b, "🔴 机器掉线：%s", sub.name)
		if sub.seen != nil {
			fmt.Fprintf(&b, "\n最后在线：%s", sub.seen.In(trafficZone).Format("01-02 15:04"))
		}
		if len(sub.routes) > 0 {
			fmt.Fprintf(&b, "\n受影响链路：%s", strings.Join(sub.routes, "、"))
		}
	case sub.kind == alertNode:
		fmt.Fprintf(&b, "🟢 机器恢复在线：%s（离线 %s）", sub.name, describeOutage(ev.lasted))
	case sub.kind == alertSpike:
		b.WriteString(spikeEventText(ev))
	case sub.kind == alertLatency && ev.failing:
		fmt.Fprintf(&b, "🟠 线路质量异常：%s", sub.name)
		for _, reason := range sub.reasons {
			b.WriteString("\n· " + reason)
		}
	case sub.kind == alertLatency:
		fmt.Fprintf(&b, "🟢 线路质量恢复：%s（持续 %s）", sub.name, describeOutage(ev.lasted))
	case ev.failing:
		fmt.Fprintf(&b, "🔴 链路失效：%s", sub.name)
		for _, reason := range sub.reasons {
			b.WriteString("\n· " + reason)
		}
	default:
		fmt.Fprintf(&b, "🟢 链路已恢复：%s（中断 %s）", sub.name, describeOutage(ev.lasted))
	}
	return b.String()
}

// ProbeOfflineText is the notice for a probe machine gone quiet.
func ProbeOfflineText(name string, seen time.Time) string {
	return fmt.Sprintf("🔴 机器掉线：%s\n最后上报：%s", name, seen.In(trafficZone).Format("01-02 15:04"))
}

// ProbeOnlineText is the notice for one reporting again.
func ProbeOnlineText(name string, lasted time.Duration) string {
	return fmt.Sprintf("🟢 机器恢复在线：%s（离线 %s）", name, describeOutage(lasted))
}

func describeOutage(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "不到 1 分钟"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时 %d 分钟", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%d 天 %d 小时", int(d.Hours())/24, int(d.Hours())%24)
	}
}
