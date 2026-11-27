package service

import (
	"context"
	"slices"
	"time"

	"github.com/kukumi1/fluxlite/internal/store"
)

const (
	// probeWindow is how much of a link's recent history one alert round
	// judges: the agents answer a check about every minute, so three
	// minutes hold a few answers and one lost packet is not the whole story.
	probeWindow = 3 * time.Minute
	// latencyAlertHold is three windows: one bad stretch is not an outage.
	latencyAlertHold = 2*probeWindow + 30*time.Second
)

// probeLinks is each panel machine's recent picture of every latency check
// its probe agent runs: the average answer and the share lost. The agents
// are the panel's only latency source; a machine without one, or without a
// panel machine to name, is not judged.
func (s *Service) probeLinks(ctx context.Context, now time.Time) ([]store.LatencySample, error) {
	pings, err := s.store.ProbePings(ctx, "", 0, store.ProbeMinute, now.Add(-probeWindow).Unix())
	if err != nil {
		return nil, err
	}
	return s.linkSamples(ctx, pings, now.Unix(), 0)
}

// probeBaselines is each link's median over the last day, in five-minute
// steps, leaving out the last half hour so a spike under way does not raise
// its own bar. A link needs a few hours of answers to have a usual.
func (s *Service) probeBaselines(ctx context.Context, now time.Time) (map[linkKey]int, error) {
	pings, err := s.store.ProbePings(ctx, "", 0, store.ProbeFive, now.Add(-spikeBaselineSpan).Unix())
	if err != nil {
		return nil, err
	}
	until := now.Add(-spikeBaselineGap).Unix()
	pings = slices.DeleteFunc(pings, func(p store.ProbePing) bool { return p.Bucket >= until })
	steps := map[linkKey][]int{}
	samples, err := s.linkSamples(ctx, pings, 0, store.ProbeFive)
	if err != nil {
		return nil, err
	}
	for _, l := range samples {
		if l.AvgMS != nil {
			k := linkKey{l.NodeID, l.Target, l.Family}
			steps[k] = append(steps[k], *l.AvgMS)
		}
	}
	out := make(map[linkKey]int, len(steps))
	for k, ms := range steps {
		if len(ms) >= spikeBaselineRounds {
			out[k] = median(ms)
		}
	}
	return out, nil
}

// linkSamples turns probe answers into the panel's link samples, named by
// panel machine and check. With step zero each link's answers are added
// into one sample at `at`; otherwise each bucket is its own sample.
func (s *Service) linkSamples(ctx context.Context, pings []store.ProbePing, at, step int64) ([]store.LatencySample, error) {
	clients, err := s.store.ProbeClients(ctx)
	if err != nil {
		return nil, err
	}
	tasks, err := s.store.ProbeTasks(ctx)
	if err != nil {
		return nil, err
	}
	nodeOf := map[string]int64{}
	for _, c := range clients {
		if c.NodeID != nil {
			nodeOf[c.UUID] = *c.NodeID
		}
	}
	nameOf := map[int64]string{}
	for _, t := range tasks {
		nameOf[t.ID] = t.Name
	}
	type key struct {
		node, bucket int64
		task         string
	}
	sums := map[key]*store.ProbePing{}
	var order []key
	for _, p := range pings {
		node, ok := nodeOf[p.Client]
		name, named := nameOf[p.Task]
		if !ok || !named {
			continue
		}
		k := key{node: node, task: name}
		if step != 0 {
			k.bucket = p.Bucket
		}
		sum := sums[k]
		if sum == nil {
			sum = &store.ProbePing{}
			sums[k] = sum
			order = append(order, k)
		}
		sum.Sent += p.Sent
		sum.Lost += p.Lost
		sum.Sum += p.Sum
	}
	out := make([]store.LatencySample, 0, len(order))
	for _, k := range order {
		sum := sums[k]
		if sum.Sent == 0 {
			continue
		}
		l := store.LatencySample{NodeID: k.node, Target: k.task, At: max(at, k.bucket), Loss: int(sum.Lost * 100 / sum.Sent)}
		if ok := sum.Sent - sum.Lost; ok > 0 {
			ms := int(sum.Sum/float64(ok) + 0.5)
			l.AvgMS = &ms
		}
		out = append(out, l)
	}
	return out, nil
}
