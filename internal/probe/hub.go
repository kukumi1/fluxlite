// Package probe takes reports from Komari agents: what each machine is doing
// now, its history by the minute, five minutes and hour, and the latency
// checks the panel has the agents run. It speaks Komari's agent protocol, so
// agents already installed for Komari report here once their endpoint does.
package probe

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/kukumi1/fluxlite/internal/store"
)

// Report is what an agent sends every few seconds, as Komari defines it.
type Report struct {
	CPU struct {
		Usage float64 `json:"usage"`
	} `json:"cpu"`
	RAM struct {
		Total int64 `json:"total"`
		Used  int64 `json:"used"`
	} `json:"ram"`
	Swap struct {
		Total int64 `json:"total"`
		Used  int64 `json:"used"`
	} `json:"swap"`
	Load struct {
		Load1  float64 `json:"load1"`
		Load5  float64 `json:"load5"`
		Load15 float64 `json:"load15"`
	} `json:"load"`
	Disk struct {
		Total int64 `json:"total"`
		Used  int64 `json:"used"`
	} `json:"disk"`
	Network struct {
		Up        int64 `json:"up"`
		Down      int64 `json:"down"`
		TotalUp   int64 `json:"totalUp"`
		TotalDown int64 `json:"totalDown"`
	} `json:"network"`
	Connections struct {
		TCP int64 `json:"tcp"`
		UDP int64 `json:"udp"`
	} `json:"connections"`
	Uptime  int64           `json:"uptime"`
	Process int64           `json:"process"`
	GPU     json.RawMessage `json:"gpu,omitempty"`
}

// valid rejects the readings Komari rejects, so one broken agent cannot
// skew a machine's history.
func (r *Report) valid() bool {
	return r.CPU.Usage >= 0 && r.CPU.Usage <= 100 && r.Load.Load1 >= 0 && r.Load.Load1 <= 1000 &&
		r.RAM.Used >= 0 && r.Swap.Used >= 0 && r.Disk.Used >= 0 && r.Network.Up >= 0 && r.Network.Down >= 0 &&
		r.Network.TotalUp >= 0 && r.Network.TotalDown >= 0 && r.Connections.TCP >= 0 && r.Connections.UDP >= 0 &&
		r.Process >= 0 && r.Uptime >= 0
}

// offlineAfter is how long a machine goes unheard before it counts as down;
// agents report every few seconds. A notice waits longer, as Komari's did by
// default, so an agent restarting is not reported.
const (
	offlineAfter = 30 * time.Second
	noticeAfter  = 3 * time.Minute
)

// StatusHook is told when a machine has been quiet long enough to report,
// and again when it reports after that.
type StatusHook func(ctx context.Context, client string, down bool, lastSeen time.Time, lasted time.Duration)

// Latest is a machine's newest report and when it came.
type Latest struct {
	Report Report
	At     time.Time
}

// PingResult is one answer to a latency check: milliseconds, or nil when
// the target did not answer.
type PingResult struct {
	At time.Time
	MS *float64
}

// recentPings is how much of each check's results stay in memory, for the
// public page's latest, average and loss.
const recentPings = time.Hour

type minuteKey struct {
	client string
	minute int64
}

type pingKey struct {
	task   int64
	client string
	minute int64
}

type taskClient struct {
	task   int64
	client string
}

// agentConn is an open report connection that latency checks are sent down.
type agentConn struct {
	v2   bool
	send func(ctx context.Context, msg []byte) error
}

// Hub holds every machine's live state and writes its history.
type Hub struct {
	store *store.Store
	log   *slog.Logger

	mu      sync.Mutex
	latest  map[string]*Latest
	conns   map[string]*agentConn
	samples map[minuteKey]*store.ProbeSample
	pings   map[pingKey]*store.ProbePing
	recent  map[taskClient][]PingResult
	tasks   []store.ProbeTask
	due     map[int64]time.Time
	// down holds, for each machine reported down, when it was last heard.
	down map[string]time.Time
	hook StatusHook
}

// linkEvery is how often probe machines not yet paired with the panel's
// machines are matched again, for agents and machines added since.
const linkEvery = 10 * time.Minute

func New(st *store.Store, log *slog.Logger) *Hub {
	return &Hub{
		store: st, log: log,
		latest: map[string]*Latest{}, conns: map[string]*agentConn{},
		samples: map[minuteKey]*store.ProbeSample{}, pings: map[pingKey]*store.ProbePing{},
		recent: map[taskClient][]PingResult{}, due: map[int64]time.Time{}, down: map[string]time.Time{},
	}
}

// Forget drops a deleted machine's live state.
func (h *Hub) Forget(client string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.latest, client)
	delete(h.down, client)
	for k := range h.recent {
		if k.client == client {
			delete(h.recent, k)
		}
	}
}

// SetStatusHook sets what is told of machines going down and coming back.
func (h *Hub) SetStatusHook(hook StatusHook) {
	h.mu.Lock()
	h.hook = hook
	h.mu.Unlock()
}

type statusChange struct {
	client   string
	down     bool
	lastSeen time.Time
	lasted   time.Duration
}

// watch finds machines quiet past the notice delay, and those reporting
// again after one. Only machines heard since the panel started are watched,
// so a restart does not report every machine before it reconnects.
func (h *Hub) watch(ctx context.Context, now time.Time) {
	var changes []statusChange
	h.mu.Lock()
	hook := h.hook
	for client, l := range h.latest {
		seen, wasDown := h.down[client]
		switch {
		case !wasDown && now.Sub(l.At) >= noticeAfter:
			h.down[client] = l.At
			changes = append(changes, statusChange{client: client, down: true, lastSeen: l.At})
		case wasDown && l.At.After(seen):
			delete(h.down, client)
			changes = append(changes, statusChange{client: client, lastSeen: seen, lasted: l.At.Sub(seen)})
		}
	}
	h.mu.Unlock()
	if hook == nil {
		return
	}
	for _, c := range changes {
		hook(ctx, c.client, c.down, c.lastSeen, c.lasted)
	}
}

// ReloadTasks reads the latency checks again after they change.
func (h *Hub) ReloadTasks(ctx context.Context) error {
	tasks, err := h.store.ProbeTasks(ctx)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.tasks = tasks
	h.mu.Unlock()
	return nil
}

// Run sends the latency checks as they fall due and writes finished minutes,
// until ctx ends; then it writes what it still holds.
func (h *Hub) Run(ctx context.Context) {
	if err := h.store.PruneProbeTaskClients(ctx); err != nil {
		h.log.Error("drop deleted machines from latency checks", "error", err)
	}
	if err := h.store.SyncProbeWeights(ctx); err != nil {
		h.log.Error("order probe machines", "error", err)
	}
	if err := h.ReloadTasks(ctx); err != nil {
		h.log.Error("load probe tasks", "error", err)
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastFlush, lastPrune, lastLink := time.Now(), time.Time{}, time.Time{}
	for {
		select {
		case <-ctx.Done():
			h.flush(context.Background(), time.Now().Add(time.Minute))
			return
		case now := <-tick.C:
			h.dispatch(ctx, now)
			if now.Sub(lastFlush) >= 10*time.Second {
				h.flush(ctx, now)
				h.watch(ctx, now)
				lastFlush = now
			}
			if now.Sub(lastPrune) >= time.Hour {
				if err := h.store.PruneProbe(ctx, now); err != nil {
					h.log.Error("prune probe history", "error", err)
				}
				lastPrune = now
			}
			// Matching may wait on name lookups, which must not hold up the
			// checks due this second.
			if now.Sub(lastLink) >= linkEvery {
				go h.Link(ctx)
				lastLink = now
			}
		}
	}
}

// dispatch sends each check that is due to every connected machine it is
// assigned to. Komari's agents run one measurement per message.
func (h *Hub) dispatch(ctx context.Context, now time.Time) {
	type send struct {
		conn *agentConn
		task store.ProbeTask
	}
	var out []send
	h.mu.Lock()
	for _, t := range h.tasks {
		if next, ok := h.due[t.ID]; ok && now.Before(next) {
			continue
		}
		h.due[t.ID] = now.Add(time.Duration(max(t.Interval, 1)) * time.Second)
		for _, c := range t.Clients {
			if conn := h.conns[c]; conn != nil {
				out = append(out, send{conn, t})
			}
		}
	}
	h.mu.Unlock()
	for _, s := range out {
		msg := pingMessage(s.conn.v2, s.task)
		sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := s.conn.send(sendCtx, msg); err != nil {
			h.log.Debug("send probe ping", "task", s.task.ID, "error", err)
		}
		cancel()
	}
}

func pingMessage(v2 bool, t store.ProbeTask) []byte {
	params := map[string]any{"ping_task_id": t.ID, "ping_type": t.Type, "ping_target": t.Target}
	var msg any = map[string]any{"jsonrpc": "2.0", "method": "agent.ping", "params": params}
	if !v2 {
		params["message"] = "ping"
		msg = params
	}
	b, _ := json.Marshal(msg)
	return b
}

// record takes a report: it becomes the machine's latest and is added to the
// minute it arrived in.
func (h *Hub) record(client string, r Report, at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.latest[client] = &Latest{Report: r, At: at}
	key := minuteKey{client, at.Unix() - at.Unix()%60}
	p := h.samples[key]
	if p == nil {
		p = &store.ProbeSample{Client: client, Bucket: key.minute}
		h.samples[key] = p
	}
	p.N++
	p.CPU += r.CPU.Usage
	p.RAM += float64(r.RAM.Used)
	p.Swap += float64(r.Swap.Used)
	p.Load1 += r.Load.Load1
	p.Load5 += r.Load.Load5
	p.Load15 += r.Load.Load15
	p.Disk += float64(r.Disk.Used)
	p.NetIn += float64(r.Network.Down)
	p.NetOut += float64(r.Network.Up)
	p.Process += float64(r.Process)
	p.TCP += float64(r.Connections.TCP)
	p.UDP += float64(r.Connections.UDP)
	p.TotalUp = max(p.TotalUp, r.Network.TotalUp)
	p.TotalDown = max(p.TotalDown, r.Network.TotalDown)
}

// recordPing takes one latency result; a negative value is a lost probe.
func (h *Hub) recordPing(client string, task int64, value float64, at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := pingKey{task, client, at.Unix() - at.Unix()%60}
	p := h.pings[key]
	if p == nil {
		p = &store.ProbePing{Task: task, Client: client, Bucket: key.minute}
		h.pings[key] = p
	}
	p.Sent++
	res := PingResult{At: at}
	if value < 0 {
		p.Lost++
	} else {
		v := value
		res.MS = &v
		p.Sum += value
		if p.Min == nil || value < *p.Min {
			p.Min = &v
		}
		if p.Max == nil || value > *p.Max {
			p.Max = &v
		}
	}
	tc := taskClient{task, client}
	recent := append(h.recent[tc], res)
	cut := 0
	for cut < len(recent) && at.Sub(recent[cut].At) > recentPings {
		cut++
	}
	h.recent[tc] = recent[cut:]
}

// flush writes every minute that has ended before now.
func (h *Hub) flush(ctx context.Context, now time.Time) {
	current := now.Unix() - now.Unix()%60
	var samples []store.ProbeSample
	var pings []store.ProbePing
	h.mu.Lock()
	for k, p := range h.samples {
		if k.minute < current {
			samples = append(samples, *p)
			delete(h.samples, k)
		}
	}
	for k, p := range h.pings {
		if k.minute < current {
			pings = append(pings, *p)
			delete(h.pings, k)
		}
	}
	h.mu.Unlock()
	if len(samples) > 0 {
		if err := h.store.AddProbeSamples(ctx, samples); err != nil {
			h.log.Error("write probe samples", "error", err)
		}
	}
	if len(pings) > 0 {
		if err := h.store.AddProbePings(ctx, pings); err != nil {
			h.log.Error("write probe pings", "error", err)
		}
	}
}

// connect registers a machine's report connection; the returned function
// removes it again, unless a newer one has replaced it.
func (h *Hub) connect(client string, c *agentConn) func() {
	h.mu.Lock()
	h.conns[client] = c
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		if h.conns[client] == c {
			delete(h.conns, client)
		}
		h.mu.Unlock()
	}
}

// Status is a machine's live state for the pages.
type Status struct {
	Latest *Latest
	Online bool
	Pings  map[int64][]PingResult
}

// Snapshot is every machine's live state, by uuid.
func (h *Hub) Snapshot(now time.Time) map[string]Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]Status{}
	for client, l := range h.latest {
		copied := *l
		out[client] = Status{Latest: &copied, Online: now.Sub(l.At) < offlineAfter, Pings: map[int64][]PingResult{}}
	}
	for tc, results := range h.recent {
		st, ok := out[tc.client]
		if !ok {
			st = Status{Pings: map[int64][]PingResult{}}
		}
		st.Pings[tc.task] = slices.Clone(results)
		out[tc.client] = st
	}
	return out
}
