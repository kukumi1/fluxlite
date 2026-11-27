package komari

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"slices"
	"sort"
	"time"

	"github.com/coder/websocket"

	"github.com/kukumi1/fluxlite/internal/probe"
	"github.com/kukumi1/fluxlite/internal/store"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	errParse     = -32700
	errNoMethod  = -32601
	errBadParams = -32602
	errInternal  = -32603
)

func reply(id json.RawMessage, result any, e *rpcError) map[string]any {
	out := map[string]any{"jsonrpc": "2.0", "id": id}
	if e != nil {
		out["error"] = e
	} else {
		out["result"] = result
	}
	return out
}

// methods are the calls the theme makes. Komari namespaces them; the
// theme only ever uses these, and is told any other does not exist, which
// it takes as an older server and falls back accordingly.
func (s *Site) methods() map[string]func(context.Context, json.RawMessage, bool) (any, *rpcError) {
	return map[string]func(context.Context, json.RawMessage, bool) (any, *rpcError){
		"common:getNodesLatestStatus": s.latestStatus,
		"common:getNodes":             s.nodes,
		"common:getRecords":           s.records,
		"public:getPublicPingTasks":   s.publicTasks,
		"public:queryMetrics":         s.queryMetrics,
		"public:getPingMetricStats":   s.pingStats,
	}
}

func (s *Site) dispatch(ctx context.Context, req rpcRequest, admin bool) map[string]any {
	if req.Method == "rpc.ping" {
		if len(req.ID) == 0 {
			return nil
		}
		return reply(req.ID, "pong", nil)
	}
	fn, ok := s.methods()[req.Method]
	if !ok {
		if len(req.ID) == 0 {
			return nil
		}
		return reply(req.ID, nil, &rpcError{errNoMethod, "Method not found"})
	}
	result, e := fn(ctx, req.Params, admin)
	if len(req.ID) == 0 {
		return nil
	}
	return reply(req.ID, result, e)
}

func (s *Site) handleRPCPost(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&raw); err != nil {
		writeJSON(w, http.StatusOK, reply(nil, nil, &rpcError{errParse, "Parse error"}))
		return
	}
	admin := s.loggedIn(r)
	if len(raw) > 0 && raw[0] == '[' {
		var batch []rpcRequest
		if err := json.Unmarshal(raw, &batch); err != nil {
			writeJSON(w, http.StatusOK, reply(nil, nil, &rpcError{errParse, "Parse error"}))
			return
		}
		out := []map[string]any{}
		for _, req := range batch {
			if res := s.dispatch(r.Context(), req, admin); res != nil {
				out = append(out, res)
			}
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeJSON(w, http.StatusOK, reply(nil, nil, &rpcError{errParse, "Parse error"}))
		return
	}
	res := s.dispatch(r.Context(), req, admin)
	if res == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleRPCSocket carries the same calls over a socket the theme keeps open.
// The browser's Origin is checked, as for any socket a page can open.
func (s *Site) handleRPCSocket(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	admin := s.loggedIn(r)
	ctx := context.WithoutCancel(r.Context())
	for {
		readCtx, done := context.WithTimeout(ctx, time.Minute)
		_, msg, err := conn.Read(readCtx)
		done()
		if err != nil {
			return
		}
		var req rpcRequest
		var res map[string]any
		if json.Unmarshal(msg, &req) != nil {
			res = reply(nil, nil, &rpcError{errParse, "Parse error"})
		} else {
			res = s.dispatch(ctx, req, admin)
		}
		if res == nil {
			continue
		}
		b, _ := json.Marshal(res)
		writeCtx, done := context.WithTimeout(ctx, 10*time.Second)
		err = conn.Write(writeCtx, websocket.MessageText, b)
		done()
		if err != nil {
			return
		}
	}
}

// visible lists the machines a visitor may see: all of them for the
// operator, the unhidden ones for anyone else.
func (s *Site) visible(ctx context.Context, admin bool) ([]store.ProbeClient, *rpcError) {
	clients, err := s.store.ProbeClients(ctx)
	if err != nil {
		s.log.Error("list probe clients", "error", err)
		return nil, &rpcError{errInternal, "Failed to get client info"}
	}
	if admin {
		return clients, nil
	}
	out := clients[:0]
	for _, c := range clients {
		if !c.Hidden {
			out = append(out, c)
		}
	}
	return out, nil
}

// expiryLayouts are the ways Komari's database has written a time.
var expiryLayouts = []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05-07:00", "2006-01-02 15:04:05", "2006-01-02"}

func normaliseTime(v any) any {
	s, ok := v.(string)
	if !ok || s == "" {
		return v
	}
	for _, layout := range expiryLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return v
}

// node is a machine as Komari's Client model presents it: its description
// and display fields, without private notes, and without addresses unless
// the operator is asking, as Komari does when guests are not sent them.
func node(c store.ProbeClient, admin bool) map[string]any {
	out := map[string]any{}
	for _, raw := range []json.RawMessage{c.Info, c.Display} {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			for k, v := range m {
				if v != nil {
					out[k] = v
				}
			}
		}
	}
	for _, k := range []string{"remark", "version", "token"} {
		delete(out, k)
	}
	if !admin {
		delete(out, "ipv4")
		delete(out, "ipv6")
	}
	out["expired_at"] = normaliseTime(out["expired_at"])
	out["uuid"], out["name"], out["weight"], out["hidden"] = c.UUID, c.Name, c.Weight, c.Hidden
	out["created_at"] = time.Unix(c.CreatedAt, 0).UTC().Format(time.RFC3339)
	out["updated_at"] = time.Unix(c.UpdatedAt, 0).UTC().Format(time.RFC3339)
	for _, k := range []string{"region", "public_remark", "group", "tags", "currency", "traffic_limit_type", "cpu_name", "os", "arch", "virtualization", "kernel_version", "gpu_name"} {
		if _, ok := out[k]; !ok {
			out[k] = ""
		}
	}
	return out
}

func (s *Site) nodes(ctx context.Context, params json.RawMessage, admin bool) (any, *rpcError) {
	var p struct {
		UUID string `json:"uuid"`
	}
	_ = json.Unmarshal(params, &p)
	clients, e := s.visible(ctx, admin)
	if e != nil {
		return nil, e
	}
	out := map[string]any{}
	for _, c := range clients {
		if p.UUID != "" && c.UUID == p.UUID {
			return node(c, admin), nil
		}
		out[c.UUID] = node(c, admin)
	}
	if p.UUID != "" {
		return nil, &rpcError{errBadParams, "Node not found"}
	}
	return out, nil
}

// pingStat is a check's last hour on one machine, as Komari summarises it.
type pingStat struct {
	Name   string  `json:"name"`
	Latest int     `json:"latest"`
	Avg    int     `json:"avg"`
	Tail   float64 `json:"tail"`
	Loss   float64 `json:"loss"`
	Min    int     `json:"min"`
	Max    int     `json:"max"`
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := float64(len(sorted)-1) * p
	lo, hi := int(math.Floor(pos)), int(math.Ceil(pos))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

func summarise(name string, results []probe.PingResult) pingStat {
	st := pingStat{Name: name, Latest: -1}
	var values []float64
	lost := 0
	for _, r := range results {
		if r.MS == nil {
			lost++
			continue
		}
		values = append(values, *r.MS)
		st.Latest = int(math.Round(*r.MS))
	}
	if len(results) > 0 {
		st.Loss = float64(lost) / float64(len(results)) * 100
	}
	if len(values) == 0 {
		return st
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	sort.Float64s(values)
	st.Avg = int(math.Round(sum / float64(len(values))))
	st.Min, st.Max = int(math.Round(values[0])), int(math.Round(values[len(values)-1]))
	if p50 := percentile(values, 0.5); p50 > 0 {
		st.Tail = (percentile(values, 0.99) - p50) / p50
	}
	return st
}

func (s *Site) latestStatus(ctx context.Context, params json.RawMessage, admin bool) (any, *rpcError) {
	var p struct {
		UUID  string   `json:"uuid"`
		UUIDs []string `json:"uuids"`
	}
	_ = json.Unmarshal(params, &p)
	clients, e := s.visible(ctx, admin)
	if e != nil {
		return nil, e
	}
	tasks, err := s.store.ProbeTasks(ctx)
	if err != nil {
		return nil, &rpcError{errInternal, "Failed to get ping tasks"}
	}
	live := s.hub.Snapshot(time.Now())
	out := map[string]any{}
	for _, c := range clients {
		st, ok := live[c.UUID]
		if !ok || st.Latest == nil {
			continue
		}
		if p.UUID != "" && c.UUID != p.UUID || len(p.UUIDs) > 0 && !slices.Contains(p.UUIDs, c.UUID) {
			continue
		}
		pings := map[string]pingStat{}
		for _, t := range tasks {
			if results := st.Pings[t.ID]; len(results) > 0 && slices.Contains(t.Clients, c.UUID) {
				pings[jsonInt(t.ID)] = summarise(t.Name, results)
			}
		}
		r := st.Latest.Report
		out[c.UUID] = map[string]any{
			"client": c.UUID, "time": st.Latest.At.UTC().Format(time.RFC3339Nano),
			"cpu": r.CPU.Usage, "gpu": 0, "temp": 0,
			"ram": r.RAM.Used, "ram_total": r.RAM.Total, "swap": r.Swap.Used, "swap_total": r.Swap.Total,
			"load": r.Load.Load1, "load5": r.Load.Load5, "load15": r.Load.Load15,
			"disk": r.Disk.Used, "disk_total": r.Disk.Total,
			"net_in": r.Network.Down, "net_out": r.Network.Up, "net_total_up": r.Network.TotalUp, "net_total_down": r.Network.TotalDown,
			"process": r.Process, "connections": r.Connections.TCP + r.Connections.UDP, "connections_udp": r.Connections.UDP,
			"online": st.Online, "uptime": r.Uptime, "ping": pings,
		}
	}
	if p.UUID != "" {
		if v, ok := out[p.UUID]; ok {
			return v, nil
		}
		return nil, &rpcError{errBadParams, "Node not found"}
	}
	return out, nil
}

func jsonInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (s *Site) publicTasks(ctx context.Context, _ json.RawMessage, admin bool) (any, *rpcError) {
	clients, e := s.visible(ctx, admin)
	if e != nil {
		return nil, e
	}
	shown := map[string]bool{}
	for _, c := range clients {
		shown[c.UUID] = true
	}
	tasks, err := s.store.ProbeTasks(ctx)
	if err != nil {
		return nil, &rpcError{errInternal, "Failed to get ping tasks"}
	}
	out := []map[string]any{}
	for _, t := range tasks {
		var on []string
		for _, c := range t.Clients {
			if shown[c] {
				on = append(on, c)
			}
		}
		if len(on) == 0 {
			continue
		}
		out = append(out, map[string]any{"id": t.ID, "name": t.Name, "type": t.Type, "interval": t.Interval, "weight": t.Weight, "clients": on})
	}
	return out, nil
}

// window is the span a records call asks for.
func window(raw json.RawMessage, now time.Time) (from, to time.Time, p recordParams, e *rpcError) {
	if err := json.Unmarshal(raw, &p); err != nil {
		return from, to, p, &rpcError{errBadParams, "Invalid params"}
	}
	to = now
	if p.End != nil {
		to = *p.End
	}
	switch {
	case p.Start != nil:
		from = *p.Start
	case p.Hours > 0:
		from = to.Add(-time.Duration(p.Hours) * time.Hour)
	default:
		from = to.Add(-time.Hour)
	}
	if p.MaxCount == 0 {
		p.MaxCount = 4000
	}
	return from, to, p, nil
}

type recordParams struct {
	Type     string     `json:"type"`
	UUID     string     `json:"uuid"`
	Hours    int        `json:"hours"`
	Start    *time.Time `json:"start"`
	End      *time.Time `json:"end"`
	TaskID   int64      `json:"task_id"`
	MaxCount int        `json:"maxCount"`
}

// resolution picks the finest buckets kept for the whole of a span: minutes
// cover the last day, five minutes and hours the last week.
func resolution(from, now time.Time) int64 {
	switch span := now.Sub(from); {
	case span <= 6*time.Hour:
		return store.ProbeMinute
	case span <= 3*24*time.Hour:
		return store.ProbeFive
	}
	return store.ProbeHour
}

// thin keeps at most n of a machine's points, evenly spread.
func thin[T any](points []T, n int) []T {
	if n <= 0 || len(points) <= n {
		return points
	}
	out := make([]T, 0, n)
	for i := range n {
		out = append(out, points[i*len(points)/n])
	}
	return out
}

func (s *Site) records(ctx context.Context, raw json.RawMessage, admin bool) (any, *rpcError) {
	now := time.Now()
	from, to, p, e := window(raw, now)
	if e != nil {
		return nil, e
	}
	clients, e := s.visible(ctx, admin)
	if e != nil {
		return nil, e
	}
	byID := map[string]store.ProbeClient{}
	for _, c := range clients {
		byID[c.UUID] = c
	}
	if p.UUID != "" {
		if _, ok := byID[p.UUID]; !ok {
			return nil, &rpcError{errBadParams, "UUID not found"}
		}
	}
	res := resolution(from, now)
	span := map[string]any{"from": from.UTC().Format(time.RFC3339), "to": to.UTC().Format(time.RFC3339)}
	if p.Type == "ping" {
		return s.pingRecords(ctx, p, byID, res, from, to, span)
	}
	samples, err := s.store.ProbeSamples(ctx, p.UUID, res, from.Unix()-res)
	if err != nil {
		return nil, &rpcError{errInternal, "Failed to fetch records"}
	}
	live := s.hub.Snapshot(now)
	grouped := map[string][]map[string]any{}
	for _, x := range samples {
		c, ok := byID[x.Client]
		if !ok || x.N == 0 || x.Bucket > to.Unix() {
			continue
		}
		n := float64(x.N)
		var totals struct {
			Mem  int64 `json:"mem_total"`
			Swap int64 `json:"swap_total"`
			Disk int64 `json:"disk_total"`
		}
		_ = json.Unmarshal(c.Info, &totals)
		if l := live[c.UUID].Latest; l != nil {
			totals.Mem, totals.Swap, totals.Disk = l.Report.RAM.Total, l.Report.Swap.Total, l.Report.Disk.Total
		}
		grouped[x.Client] = append(grouped[x.Client], map[string]any{
			"client": x.Client, "time": time.Unix(x.Bucket, 0).UTC().Format(time.RFC3339),
			"cpu": x.CPU / n, "gpu": 0, "temp": 0,
			"ram": int64(x.RAM / n), "ram_total": totals.Mem, "swap": int64(x.Swap / n), "swap_total": totals.Swap,
			"load": x.Load1 / n, "disk": int64(x.Disk / n), "disk_total": totals.Disk,
			"net_in": int64(x.NetIn / n), "net_out": int64(x.NetOut / n),
			"net_total_up": x.TotalUp, "net_total_down": x.TotalDown,
			"process": int(x.Process / n), "connections": int((x.TCP + x.UDP) / n), "connections_udp": int(x.UDP / n),
		})
	}
	count := 0
	for k, v := range grouped {
		if p.MaxCount > 0 && len(grouped) > 0 {
			v = thin(v, max(1, p.MaxCount/len(grouped)))
		}
		grouped[k] = v
		count += len(v)
	}
	span["count"], span["records"] = count, grouped
	return span, nil
}

// pingRecords gives one record per bucket: the average answer, how many
// were sent and the share lost, which the theme reads back into loss.
func (s *Site) pingRecords(ctx context.Context, p recordParams, byID map[string]store.ProbeClient, res int64, from, to time.Time, span map[string]any) (any, *rpcError) {
	task := p.TaskID
	if task < 0 {
		task = 0
	}
	pings, err := s.store.ProbePings(ctx, p.UUID, task, res, from.Unix()-res)
	if err != nil {
		return nil, &rpcError{errInternal, "Failed to fetch ping records"}
	}
	tasks, err := s.store.ProbeTasks(ctx)
	if err != nil {
		return nil, &rpcError{errInternal, "Failed to fetch ping tasks"}
	}
	type acc struct {
		sent, lost int64
		sum        float64
		min, max   float64
		latest     float64
		values     []float64
	}
	perTask := map[int64]*acc{}
	perClient := map[string]*acc{}
	var records []map[string]any
	for _, x := range pings {
		if _, ok := byID[x.Client]; !ok || x.Sent == 0 || x.Bucket > to.Unix() {
			continue
		}
		value := -1.0
		if valid := x.Sent - x.Lost; valid > 0 {
			value = math.Round(x.Sum/float64(valid)*10) / 10
		}
		records = append(records, map[string]any{
			"task_id": x.Task, "client": x.Client, "time": time.Unix(x.Bucket, 0).UTC().Format(time.RFC3339),
			"value": value, "count": x.Sent, "loss": float64(x.Lost) / float64(x.Sent) * 100,
		})
		add := func(a *acc) {
			a.sent += x.Sent
			a.lost += x.Lost
			a.sum += x.Sum
			if x.Min != nil && (a.min == 0 || *x.Min < a.min) {
				a.min = *x.Min
			}
			if x.Max != nil && *x.Max > a.max {
				a.max = *x.Max
			}
			if value >= 0 {
				a.latest = value
				a.values = append(a.values, value)
			}
		}
		if perTask[x.Task] == nil {
			perTask[x.Task] = &acc{}
		}
		if perClient[x.Client] == nil {
			perClient[x.Client] = &acc{}
		}
		add(perTask[x.Task])
		add(perClient[x.Client])
	}
	if p.MaxCount > 0 {
		records = thin(records, p.MaxCount)
	}
	lossOf := func(a *acc) float64 {
		if a.sent == 0 {
			return 0
		}
		return float64(a.lost) / float64(a.sent) * 100
	}
	basic := []map[string]any{}
	for client, a := range perClient {
		basic = append(basic, map[string]any{"client": client, "loss": lossOf(a), "min": int(a.min), "max": int(a.max)})
	}
	list := []map[string]any{}
	for _, t := range tasks {
		if p.TaskID > 0 && t.ID != p.TaskID || p.UUID != "" && !slices.Contains(t.Clients, p.UUID) {
			continue
		}
		// The records are a bucket apart, not a check apart; the theme reads
		// a longer gap than the interval as a break in the line.
		entry := map[string]any{"id": t.ID, "name": t.Name, "type": t.Type, "interval": max(int64(t.Interval), res), "weight": t.Weight,
			"loss": 0.0, "min": 0, "max": 0, "avg": 0, "latest": -1, "total": 0, "p50": 0, "p99": 0}
		if a := perTask[t.ID]; a != nil {
			sort.Float64s(a.values)
			avg := 0
			if valid := a.sent - a.lost; valid > 0 {
				avg = int(math.Round(a.sum / float64(valid)))
			}
			entry["loss"], entry["min"], entry["max"], entry["avg"], entry["latest"], entry["total"] = lossOf(a), int(a.min), int(a.max), avg, int(a.latest), a.sent
			entry["p50"], entry["p99"] = int(percentile(a.values, 0.5)), int(percentile(a.values, 0.99))
		}
		if p.UUID == "" {
			var on []string
			for _, c := range t.Clients {
				if _, ok := byID[c]; ok {
					on = append(on, c)
				}
			}
			entry["clients"] = on
		}
		list = append(list, entry)
	}
	if records == nil {
		records = []map[string]any{}
	}
	span["count"], span["records"], span["basic_info"], span["tasks"] = len(records), records, basic, list
	return span, nil
}
