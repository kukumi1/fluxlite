package komari

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/kukumi1/fluxlite/internal/store"
)

// Komari's metric API: a series per metric and machine, in buckets of a
// standard interval. The theme prefers it to the records calls for history.

const defaultMetricPoints = 500

// standardIntervals are the bucket sizes Komari rounds a query's up to.
var standardIntervals = []int64{60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400}

type metricParams struct {
	MetricKey           string            `json:"metric_key"`
	MetricKeys          []string          `json:"metric_keys"`
	Metrics             []string          `json:"metrics"`
	EntityID            string            `json:"entity_id"`
	EntityIDs           []string          `json:"entity_ids"`
	Start               *time.Time        `json:"start"`
	StartTime           *time.Time        `json:"start_time"`
	End                 *time.Time        `json:"end"`
	EndTime             *time.Time        `json:"end_time"`
	Hours               float64           `json:"hours"`
	Tags                map[string]string `json:"tags"`
	MaxPoints           int               `json:"max_points"`
	MaxPointsByMetric   map[string]int    `json:"max_points_by_metric"`
	Aggregation         string            `json:"aggregation"`
	AggregationByMetric map[string]string `json:"aggregation_by_metric"`
	TaskID              any               `json:"task_id"`
	TaskIDs             []any             `json:"task_ids"`
}

func (p metricParams) span(now time.Time) (time.Time, time.Time) {
	end := now
	for _, t := range []*time.Time{p.End, p.EndTime} {
		if t != nil {
			end = *t
			break
		}
	}
	hours := p.Hours
	if hours <= 0 {
		hours = 1
	}
	start := end.Add(-time.Duration(hours * float64(time.Hour)))
	for _, t := range []*time.Time{p.Start, p.StartTime} {
		if t != nil {
			start = *t
			break
		}
	}
	return start, end
}

func nonEmpty(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		for _, v := range l {
			if v != "" && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	return out
}

// interval is the bucket a query is answered in: wide enough to keep within
// its points, and no finer than a resolution whose history reaches back to
// its start. Imported history is coarser the older it is, so a week may be
// answered in hours until the panel has kept five-minute buckets that long.
func interval(start, end, now time.Time, maxPoints int, earliest map[int64]int64) (step, res int64) {
	if maxPoints <= 0 {
		maxPoints = defaultMetricPoints
	}
	want := int64(math.Ceil(end.Sub(start).Seconds() / float64(maxPoints)))
	step = standardIntervals[len(standardIntervals)-1]
	for _, s := range standardIntervals {
		if s >= want {
			step = s
			break
		}
	}
	finest := int64(store.ProbeHour)
	// A finer resolution will do when it reaches the start, or reaches as
	// far back as anything does.
	oldest, haveHours := earliest[store.ProbeHour]
	for _, r := range []int64{store.ProbeMinute, store.ProbeFive} {
		if first, ok := earliest[r]; ok && (first <= start.Unix()+r || !haveHours || first <= oldest+store.ProbeHour) {
			finest = r
			break
		}
	}
	res = finest
	for _, r := range []int64{store.ProbeHour, store.ProbeFive, store.ProbeMinute} {
		if r >= finest && step%r == 0 {
			res = r
			break
		}
	}
	return max(step, res), res
}

type metricPoint struct {
	Time  string   `json:"time"`
	Value *float64 `json:"value"`
	Count int64    `json:"count"`
}

type metricSeries struct {
	MetricKey           string            `json:"metric_key"`
	EntityID            string            `json:"entity_id"`
	Tags                map[string]string `json:"tags,omitempty"`
	Downsampled         bool              `json:"downsampled"`
	DownsampleAlgorithm string            `json:"downsample_algorithm,omitempty"`
	MaxPoints           int               `json:"max_points,omitempty"`
	IntervalSeconds     float64           `json:"interval_seconds"`
	Count               int               `json:"count"`
	Points              []metricPoint     `json:"points"`
}

func num(v float64) *float64 { return &v }

func bucketTime(b int64) string { return time.Unix(b, 0).UTC().Format(time.RFC3339) }

// loadValue reads one metric from a sample, as a per-report average.
var loadValue = map[string]func(store.ProbeSample) float64{
	"cpu.usage":       func(p store.ProbeSample) float64 { return p.CPU / float64(p.N) },
	"memory.used":     func(p store.ProbeSample) float64 { return p.RAM / float64(p.N) },
	"swap.used":       func(p store.ProbeSample) float64 { return p.Swap / float64(p.N) },
	"load.average":    func(p store.ProbeSample) float64 { return p.Load1 / float64(p.N) },
	"disk.used":       func(p store.ProbeSample) float64 { return p.Disk / float64(p.N) },
	"net.in.rate":     func(p store.ProbeSample) float64 { return p.NetIn / float64(p.N) },
	"net.out.rate":    func(p store.ProbeSample) float64 { return p.NetOut / float64(p.N) },
	"process.count":   func(p store.ProbeSample) float64 { return p.Process / float64(p.N) },
	"connections.tcp": func(p store.ProbeSample) float64 { return p.TCP / float64(p.N) },
	"connections.udp": func(p store.ProbeSample) float64 { return p.UDP / float64(p.N) },
	"net.total.up":    func(p store.ProbeSample) float64 { return float64(p.TotalUp) },
	"net.total.down":  func(p store.ProbeSample) float64 { return float64(p.TotalDown) },
}

// traffic is the bytes moved in a bucket: the rise of the agent's running
// total since the bucket before, or the whole total when it restarted.
var traffic = map[string]func(store.ProbeSample) int64{
	"traffic.up":   func(p store.ProbeSample) int64 { return p.TotalUp },
	"traffic.down": func(p store.ProbeSample) int64 { return p.TotalDown },
}

func (s *Site) queryMetrics(ctx context.Context, raw json.RawMessage, admin bool) (any, *rpcError) {
	var p metricParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{errBadParams, "Invalid request body"}
	}
	keys := nonEmpty(p.MetricKeys, p.Metrics, []string{p.MetricKey})
	if len(keys) == 0 {
		return nil, &rpcError{errBadParams, "metric_keys is required"}
	}
	now := time.Now()
	start, end := p.span(now)
	if !end.After(start) {
		return nil, &rpcError{errBadParams, "end must be after start"}
	}
	clients, e := s.visible(ctx, admin)
	if e != nil {
		return nil, e
	}
	entities := s.entities(clients, nonEmpty(p.EntityIDs, []string{p.EntityID}))
	earliest, err := s.historyStart(ctx)
	if err != nil {
		return nil, &rpcError{errInternal, "Failed to query metrics"}
	}

	var series []metricSeries
	for _, key := range keys {
		maxPoints := p.MaxPoints
		if v, ok := p.MaxPointsByMetric[key]; ok && v > 0 {
			maxPoints = v
		}
		step, res := interval(start, end, now, maxPoints, earliest)
		agg := p.Aggregation
		if v := p.AggregationByMetric[key]; v != "" {
			agg = v
		}
		if agg == "" {
			agg = "avg"
		}
		base := metricSeries{MetricKey: key, Downsampled: true, DownsampleAlgorithm: agg, MaxPoints: maxPoints, IntervalSeconds: float64(step)}
		var out []metricSeries
		switch {
		case key == "ping.latency_ms" || key == "ping.loss":
			out, err = s.pingSeries(ctx, base, entities, p.Tags, res, step, start, end)
		default:
			out, err = s.loadSeries(ctx, base, entities, agg, res, step, start, end)
		}
		if err != nil {
			s.log.Error("query probe metrics", "metric", key, "error", err)
			return nil, &rpcError{errInternal, "Failed to query metrics"}
		}
		series = append(series, out...)
	}
	if series == nil {
		series = []metricSeries{}
	}
	return map[string]any{"start": start.UTC().Format(time.RFC3339Nano), "end": end.UTC().Format(time.RFC3339Nano),
		"server_downsample_default": true, "default_points": defaultMetricPoints, "series": series, "count": len(series)}, nil
}

// entities are the machines a query may read: those asked for that the
// visitor can see, or all they can see.
func (s *Site) entities(clients []store.ProbeClient, asked []string) []string {
	var out []string
	for _, c := range clients {
		if len(asked) == 0 || slices.Contains(asked, c.UUID) {
			out = append(out, c.UUID)
		}
	}
	return out
}

func (s *Site) loadSeries(ctx context.Context, base metricSeries, entities []string, agg string, res, step int64, start, end time.Time) ([]metricSeries, error) {
	value, isLoad := loadValue[base.MetricKey]
	total, isTraffic := traffic[base.MetricKey]
	var out []metricSeries
	for _, id := range entities {
		item := base
		item.EntityID = id
		item.Points = []metricPoint{}
		if isLoad || isTraffic {
			samples, err := s.store.ProbeSamples(ctx, id, res, start.Unix()-step-res)
			if err != nil {
				return nil, err
			}
			item.Points = aggregateLoad(samples, value, total, isTraffic, agg, step, start.Unix(), end.Unix())
		}
		item.Count = len(item.Points)
		out = append(out, item)
	}
	return out, nil
}

func aggregateLoad(samples []store.ProbeSample, value func(store.ProbeSample) float64, total func(store.ProbeSample) int64, isTraffic bool, agg string, step, from, to int64) []metricPoint {
	type bucket struct {
		n              int64
		sum, max, last float64
		set            bool
	}
	buckets := map[int64]*bucket{}
	var order []int64
	prev := int64(-1)
	for _, x := range samples {
		if x.N == 0 {
			continue
		}
		var v float64
		if isTraffic {
			t := total(x)
			switch {
			case prev < 0:
				prev = t
				continue
			case t >= prev:
				v = float64(t - prev)
			default:
				v = float64(t)
			}
			prev = t
		} else {
			v = value(x)
		}
		if x.Bucket < from-step || x.Bucket > to {
			continue
		}
		key := x.Bucket - x.Bucket%step
		b := buckets[key]
		if b == nil {
			b = &bucket{}
			buckets[key] = b
			order = append(order, key)
		}
		b.n += x.N
		if isTraffic {
			b.sum += v
		} else {
			b.sum += v * float64(x.N)
		}
		if !b.set || v > b.max {
			b.max = v
		}
		b.last, b.set = v, true
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	points := make([]metricPoint, 0, len(order))
	for _, key := range order {
		b := buckets[key]
		var v float64
		switch {
		case isTraffic || agg == "sum":
			v = b.sum
		case agg == "last":
			v = b.last
		case agg == "max":
			v = b.max
		default:
			v = b.sum / float64(b.n)
		}
		points = append(points, metricPoint{Time: bucketTime(key), Value: num(v), Count: b.n})
	}
	return points
}

type pingBucket struct {
	sent, lost int64
	sum        float64
	min, max   float64
	hasRange   bool
}

func (b *pingBucket) add(x store.ProbePing) {
	b.sent += x.Sent
	b.lost += x.Lost
	b.sum += x.Sum
	if x.Min != nil && (!b.hasRange || *x.Min < b.min) {
		b.min = *x.Min
	}
	if x.Max != nil && (!b.hasRange || *x.Max > b.max) {
		b.max = *x.Max
	}
	if x.Min != nil && x.Max != nil {
		b.hasRange = true
	}
}

type seriesKey struct {
	task   int64
	client string
}

// pingBuckets groups latency history by check and machine, then by bucket.
func (s *Site) pingBuckets(ctx context.Context, entities []string, tasks map[int64]bool, res, step int64, start, end time.Time) (map[seriesKey]map[int64]*pingBucket, error) {
	out := map[seriesKey]map[int64]*pingBucket{}
	for _, id := range entities {
		pings, err := s.store.ProbePings(ctx, id, 0, res, start.Unix()-res)
		if err != nil {
			return nil, err
		}
		for _, x := range pings {
			if x.Sent == 0 || x.Bucket > end.Unix() || len(tasks) > 0 && !tasks[x.Task] {
				continue
			}
			k := seriesKey{x.Task, x.Client}
			if out[k] == nil {
				out[k] = map[int64]*pingBucket{}
			}
			key := x.Bucket - x.Bucket%step
			b := out[k][key]
			if b == nil {
				b = &pingBucket{}
				out[k][key] = b
			}
			b.add(x)
		}
	}
	return out, nil
}

func sortedBuckets(m map[int64]*pingBucket) []int64 {
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// pingSeries answers as Komari stores latency: the average counts a lost
// probe as -1, and loss is the share of probes lost. The theme takes the
// -1s back out using the loss.
func (s *Site) pingSeries(ctx context.Context, base metricSeries, entities []string, tags map[string]string, res, step int64, start, end time.Time) ([]metricSeries, error) {
	only := map[int64]bool{}
	if v := tags["task_id"]; v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return []metricSeries{}, nil
		}
		only[id] = true
	}
	grouped, err := s.pingBuckets(ctx, entities, only, res, step, start, end)
	if err != nil {
		return nil, err
	}
	keys := make([]seriesKey, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].client != keys[j].client {
			return keys[i].client < keys[j].client
		}
		return keys[i].task < keys[j].task
	})
	var out []metricSeries
	for _, k := range keys {
		item := base
		item.EntityID = k.client
		item.Tags = map[string]string{"task_id": strconv.FormatInt(k.task, 10)}
		for _, b := range sortedBuckets(grouped[k]) {
			x := grouped[k][b]
			var v float64
			if base.MetricKey == "ping.loss" {
				v = float64(x.lost) / float64(x.sent)
			} else if x.lost == x.sent {
				v = -1
			} else {
				v = (x.sum - float64(x.lost)) / float64(x.sent)
			}
			item.Points = append(item.Points, metricPoint{Time: bucketTime(b), Value: num(v), Count: x.sent})
		}
		item.Count = len(item.Points)
		out = append(out, item)
	}
	return out, nil
}

func taskFilter(one any, many []any) map[int64]bool {
	out := map[int64]bool{}
	for _, v := range append([]any{one}, many...) {
		switch t := v.(type) {
		case float64:
			out[int64(t)] = true
		case string:
			if id, err := strconv.ParseInt(t, 10, 64); err == nil {
				out[id] = true
			}
		}
	}
	return out
}

func (s *Site) pingStats(ctx context.Context, raw json.RawMessage, admin bool) (any, *rpcError) {
	var p metricParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{errBadParams, "Invalid request body"}
	}
	now := time.Now()
	start, end := p.span(now)
	clients, e := s.visible(ctx, admin)
	if e != nil {
		return nil, e
	}
	tasks, err := s.store.ProbeTasks(ctx)
	if err != nil {
		return nil, &rpcError{errInternal, "Failed to get ping tasks"}
	}
	byID := map[int64]store.ProbeTask{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	earliest, err := s.historyStart(ctx)
	if err != nil {
		return nil, &rpcError{errInternal, "Failed to query ping stats"}
	}
	step, res := interval(start, end, now, p.MaxPoints, earliest)
	grouped, err := s.pingBuckets(ctx, s.entities(clients, nonEmpty(p.EntityIDs, []string{p.EntityID})), taskFilter(p.TaskID, p.TaskIDs), res, step, start, end)
	if err != nil {
		return nil, &rpcError{errInternal, "Failed to query ping stats"}
	}
	stats := []map[string]any{}
	for k, buckets := range grouped {
		t, ok := byID[k.task]
		if !ok || !slices.Contains(t.Clients, k.client) {
			continue
		}
		var total pingBucket
		var avgs []float64
		latest := -1.0
		for _, b := range sortedBuckets(buckets) {
			x := buckets[b]
			total.sent += x.sent
			total.lost += x.lost
			total.sum += x.sum
			if x.hasRange {
				if !total.hasRange || x.min < total.min {
					total.min = x.min
				}
				if !total.hasRange || x.max > total.max {
					total.max = x.max
				}
				total.hasRange = true
			}
			if valid := x.sent - x.lost; valid > 0 {
				latest = x.sum / float64(valid)
				avgs = append(avgs, latest)
			}
		}
		entry := map[string]any{"entity_id": k.client, "task_id": strconv.FormatInt(k.task, 10), "name": t.Name, "type": t.Type,
			"interval": t.Interval, "total": total.sent, "valid": total.sent - total.lost, "loss": float64(total.lost) / float64(total.sent) * 100,
			"p99_p50_ratio": 0.0}
		if valid := total.sent - total.lost; valid > 0 {
			avg := total.sum / float64(valid)
			sort.Float64s(avgs)
			p50, p99 := percentile(avgs, 0.5), percentile(avgs, 0.99)
			variance := 0.0
			for _, v := range avgs {
				variance += (v - avg) * (v - avg)
			}
			entry["avg"], entry["latest"], entry["p50"], entry["p99"] = avg, latest, p50, p99
			entry["stddev"] = math.Sqrt(variance / float64(len(avgs)))
			if total.hasRange {
				entry["min"], entry["max"] = total.min, total.max
			}
			if p50 > 0 {
				entry["p99_p50_ratio"] = p99 / p50
			}
		}
		stats = append(stats, entry)
	}
	sort.Slice(stats, func(i, j int) bool {
		a, b := stats[i], stats[j]
		if a["entity_id"] != b["entity_id"] {
			return a["entity_id"].(string) < b["entity_id"].(string)
		}
		return a["task_id"].(string) < b["task_id"].(string)
	})
	return map[string]any{"start": start.UTC().Format(time.RFC3339Nano), "end": end.UTC().Format(time.RFC3339Nano),
		"interval_seconds": step, "stats": stats, "count": len(stats)}, nil
}
