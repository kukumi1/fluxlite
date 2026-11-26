package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

// expiryWarnAhead is how long before a node expires its owner is told.
const expiryWarnAhead = 72 * time.Hour

type levelKind int

const (
	levelQuota levelKind = iota
	levelExpiry
)

// levelSubject is something that only gets worse until it is reset: an
// allowance being used up, an expiry date coming closer. Level 0 is fine,
// each higher level says something new.
type levelSubject struct {
	key   string
	kind  levelKind
	level int
	texts map[int]string
}

type levelEvent struct {
	kind levelKind
	text string
}

// levelTracker reports a subject once each time it reaches a higher level.
// Going back down — a new period, a top-up, a renewal — is recorded silently,
// so the next climb is reported again. Like alertTracker it starts primed by
// the first pass and forgets subjects that disappear.
type levelTracker struct {
	mu     sync.Mutex
	primed bool
	levels map[string]int
}

func (t *levelTracker) observe(subjects []levelSubject) []levelEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.levels == nil {
		t.levels = make(map[string]int)
	}

	var events []levelEvent
	present := make(map[string]bool, len(subjects))
	for _, sub := range subjects {
		present[sub.key] = true
		prev := t.levels[sub.key]
		t.levels[sub.key] = sub.level
		if !t.primed || sub.level <= prev {
			continue
		}
		if text := sub.texts[sub.level]; text != "" {
			events = append(events, levelEvent{kind: sub.kind, text: text})
		}
	}
	for key := range t.levels {
		if !present[key] {
			delete(t.levels, key)
		}
	}
	t.primed = true
	return events
}

func (s *Service) levelSubjects(ctx context.Context, now time.Time) ([]levelSubject, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	machine := make(map[int64]string, len(nodes))
	for _, n := range nodes {
		machine[n.ID] = n.Name
	}
	users, err := s.store.ListSingBoxUsers(ctx)
	if err != nil {
		return nil, err
	}
	var subjects []levelSubject
	for _, u := range users {
		subjects = append(subjects, singBoxLevels(u, machine[u.NodeID], now)...)
	}

	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}
	states, err := s.QuotaStates(ctx)
	if err != nil {
		return nil, err
	}
	used := make(map[int64]QuotaState, len(states))
	for _, st := range states {
		used[st.RouteID] = st
	}
	for _, r := range routes {
		if sub, ok := routeQuotaLevel(r, used[r.ID]); ok {
			subjects = append(subjects, sub)
		}
	}
	return subjects, nil
}

// singBoxLevels reports a node's allowance and expiry. A node the operator
// switched off by hand is left out: it is not running out of anything.
func singBoxLevels(u *model.SingBoxUser, machine string, now time.Time) []levelSubject {
	stoppedByPanel := u.QuotaPausedAt != nil || u.Status == model.SingBoxStatusExhausted || u.Status == model.SingBoxStatusExpired
	if !u.Enabled && !stoppedByPanel {
		return nil
	}
	label := u.Name
	if machine != "" {
		label = fmt.Sprintf("%s（%s）", u.Name, machine)
	}

	var subjects []levelSubject
	if quota := u.QuotaBytes(); quota > 0 {
		usedBytes := u.UsedBytes()
		level := 0
		switch {
		case u.QuotaPausedAt != nil || u.Status == model.SingBoxStatusExhausted || usedBytes >= quota:
			level = 2
		case usedBytes >= quota/10*9:
			level = 1
		}
		subjects = append(subjects, levelSubject{
			key: fmt.Sprintf("sbq:%d", u.ID), kind: levelQuota, level: level,
			texts: map[int]string{
				1: fmt.Sprintf("🟠 节点流量已用 %d%%：%s\n%s / %s", percentOf(usedBytes, quota), label, formatBytes(usedBytes), formatBytes(quota)),
				2: fmt.Sprintf("🔴 节点流量已用完：%s\n共 %s，面板已自动停用", label, formatBytes(quota)),
			},
		})
	}
	if !u.ExpiresAt.IsZero() {
		left := u.ExpiresAt.Sub(now)
		level := 0
		switch {
		case left <= 0 || u.Status == model.SingBoxStatusExpired:
			level = 2
		case left <= expiryWarnAhead:
			level = 1
		}
		subjects = append(subjects, levelSubject{
			key: fmt.Sprintf("sbe:%d", u.ID), kind: levelExpiry, level: level,
			texts: map[int]string{
				1: fmt.Sprintf("⏰ 节点即将到期：%s\n到期时间 %s", label, u.ExpiresAt.In(trafficZone).Format("01-02 15:04")),
				2: fmt.Sprintf("⛔ 节点已到期：%s\n面板已自动停用", label),
			},
		})
	}
	return subjects
}

// routeQuotaLevel reports a capped route's period usage. An unmeasured period
// says nothing, and a route stopped by hand is not running out.
func routeQuotaLevel(r *model.Route, st QuotaState) (levelSubject, bool) {
	if r.QuotaBytes == nil || *r.QuotaBytes <= 0 || (!r.Enabled && r.QuotaPausedAt == nil) {
		return levelSubject{}, false
	}
	quota := *r.QuotaBytes
	level := 0
	switch {
	case r.QuotaPausedAt != nil || (st.Measured && st.UsedBytes >= quota):
		level = 2
	case st.Measured && st.UsedBytes >= quota/10*9:
		level = 1
	}
	return levelSubject{
		key: fmt.Sprintf("rq:%d", r.ID), kind: levelQuota, level: level,
		texts: map[int]string{
			1: fmt.Sprintf("🟠 链路流量已用 %d%%：%s\n%s / %s", percentOf(st.UsedBytes, quota), r.Name, formatBytes(st.UsedBytes), formatBytes(quota)),
			2: fmt.Sprintf("🔴 链路流量已用完：%s\n本周期 %s，面板已自动停止，下个周期自动恢复", r.Name, formatBytes(quota)),
		},
	}, true
}

func percentOf(part, whole int64) int {
	if whole <= 0 {
		return 0
	}
	return int(float64(part) / float64(whole) * 100)
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, suffix := float64(n), []string{"KB", "MB", "GB", "TB", "PB"}
	i := -1
	for value >= unit && i < len(suffix)-1 {
		value /= unit
		i++
	}
	return fmt.Sprintf("%.2f %s", value, suffix[i])
}

// alertNotice is a message raised by work outside the digest, such as the
// daily return-route trace, waiting for the next digest to carry it.
type alertNotice struct {
	nodeID int64
	text   string
}

type noticeQueue struct {
	mu      sync.Mutex
	pending []alertNotice
}

func (s *Service) queueNotice(n alertNotice) {
	s.notices.mu.Lock()
	defer s.notices.mu.Unlock()
	s.notices.pending = append(s.notices.pending, n)
}

func (s *Service) drainNotices() []alertNotice {
	s.notices.mu.Lock()
	defer s.notices.mu.Unlock()
	out := s.notices.pending
	s.notices.pending = nil
	return out
}
