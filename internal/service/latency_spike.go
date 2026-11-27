package service

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
)

const (
	// A link's baseline is its median over the last day, leaving out the
	// last half hour so a spike under way does not raise its own bar.
	spikeBaselineSpan = 24 * time.Hour
	spikeBaselineGap  = 30 * time.Minute
	// spikeBaselineRounds is the fewest five-minute steps with an answer a
	// baseline needs; a link measured for an hour has no "usual" yet.
	spikeBaselineRounds = 20
	spikeBaselineTTL    = 10 * time.Minute
	// A round spikes at twice the baseline and at least spikeMinRiseMS
	// above it, so a 20ms link going to 45ms is not news.
	spikeFactor    = 2
	spikeMinRiseMS = 50
	spikeShown     = 6
)

type linkKey struct {
	node   int64
	target string
	family int
}

type spikeBaselines struct {
	mu  sync.Mutex
	at  time.Time
	byL map[linkKey]int
}

func (s *Service) baselines(ctx context.Context, now time.Time) (map[linkKey]int, error) {
	s.spikeBase.mu.Lock()
	defer s.spikeBase.mu.Unlock()
	if s.spikeBase.byL != nil && now.Sub(s.spikeBase.at) < spikeBaselineTTL {
		return s.spikeBase.byL, nil
	}
	byL, err := s.probeBaselines(ctx, now)
	if err != nil {
		return nil, err
	}
	s.spikeBase.at, s.spikeBase.byL = now, byL
	return byL, nil
}

type spikeReading struct {
	link     linkKey
	ms, base int
}

func (r spikeReading) spiking() bool {
	return r.ms >= r.base*spikeFactor && r.ms-r.base >= spikeMinRiseMS
}

func (r spikeReading) text(withRise bool) string {
	s := fmt.Sprintf("%s：%dms（基线 %dms", r.link.target, r.ms, r.base)
	if withRise && r.base > 0 {
		s += fmt.Sprintf("，+%d%%", (r.ms-r.base)*100/r.base)
	}
	return s + "）"
}

// spikeSubjects judges each machine by its links over both families: it is
// spiking while any link's latest round is far above that link's usual. A
// machine with no link to judge, offline or without a baseline yet, is
// unknown, which leaves whatever was last reported standing.
func (s *Service) spikeSubjects(ctx context.Context, now time.Time) ([]alertSubject, error) {
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	latest, err := s.probeLinks(ctx, now)
	if err != nil {
		return nil, err
	}
	base, err := s.baselines(ctx, now)
	if err != nil {
		return nil, err
	}
	readings := make(map[int64][]spikeReading)
	for _, l := range latest {
		key := linkKey{l.NodeID, l.Target, l.Family}
		b, ok := base[key]
		if !ok || l.AvgMS == nil || l.Loss >= 100 {
			continue
		}
		readings[l.NodeID] = append(readings[l.NodeID], spikeReading{link: key, ms: *l.AvgMS, base: b})
	}
	subjects := make([]alertSubject, 0, len(nodes))
	for _, n := range nodes {
		subjects = append(subjects, spikeSubject(n, readings[n.ID]))
	}
	return subjects, nil
}

func spikeSubject(n *model.Node, readings []spikeReading) alertSubject {
	sub := alertSubject{key: fmt.Sprintf("spike:%d", n.ID), kind: alertSpike, nodeID: n.ID, name: n.Name, hold: latencyAlertHold}
	if n.Status != model.StatusOnline || len(readings) == 0 {
		return sub
	}
	// The worst first, so a long list keeps what matters.
	slices.SortFunc(readings, func(a, b spikeReading) int {
		return b.ms*a.max1() - a.ms*b.max1()
	})
	var spiking []spikeReading
	for _, r := range readings {
		if r.spiking() {
			spiking = append(spiking, r)
		}
	}
	shown := readings
	if len(spiking) > 0 {
		sub.state, shown = obsFailing, spiking
	} else {
		sub.state = obsOK
	}
	for i, r := range shown {
		if i == spikeShown {
			sub.reasons = append(sub.reasons, fmt.Sprintf("还有 %d 条", len(shown)-spikeShown))
			break
		}
		sub.reasons = append(sub.reasons, r.text(sub.state == obsFailing))
	}
	return sub
}

// max1 is the baseline as a divisor that is never zero.
func (r spikeReading) max1() int { return max(r.base, 1) }

// spikeEventText is the message for a machine spiking or settling back.
func spikeEventText(ev alertEvent) string {
	sub := ev.subject
	var text string
	if ev.failing {
		text = fmt.Sprintf("⚠️ 延迟异常暴涨：%s\n触发：持续 6 分钟以上高于近一天基线 %d 倍（且至少高出 %dms）", sub.name, spikeFactor, spikeMinRiseMS)
	} else {
		text = fmt.Sprintf("✅ 延迟恢复正常：%s（持续 %s）\n现在：", sub.name, describeOutage(ev.lasted))
	}
	for _, r := range sub.reasons {
		text += "\n· " + r
	}
	return text
}
