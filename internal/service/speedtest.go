package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/sshx"
	"github.com/kukumi1/fluxlite/internal/store"

	"golang.org/x/crypto/ssh"
)

const (
	speedJobsKeep    = 40
	speedMaxPoints   = 9
	speedMaxNodes    = 10
	speedConcurrency = 2
	// speedRunSeconds bounds one point in one mode: queueing for a key,
	// five seconds each way, and one retry of the whole thing.
	speedRunSeconds = 150
)

// SpeedProvinces are the mainland provinces the speed test servers cover,
// north to south, east to west.
var SpeedProvinces = []string{
	"北京", "天津", "河北", "山西", "内蒙古", "辽宁", "吉林", "黑龙江",
	"上海", "江苏", "浙江", "安徽", "福建", "江西", "山东",
	"河南", "湖北", "湖南", "广东", "广西", "海南",
	"重庆", "四川", "贵州", "云南", "西藏",
	"陕西", "甘肃", "青海", "宁夏", "新疆",
}

var SpeedCarriers = []string{"电信", "联通", "移动"}

var (
	ErrSpeedRequest = errors.New("测速设置无效")
	ErrSpeedBusy    = errors.New("有机器正在测速，等它测完再开始")
)

// SpeedRequest is what the operator asked to test: each point is a
// province and carrier such as 上海电信.
type SpeedRequest struct {
	NodeIDs []int64  `json:"node_ids"`
	Points  []string `json:"points"`
	Mode    string   `json:"mode"`
	Family  int      `json:"family"`
	// Origin is "telegram" for a test started from the bot, whose result
	// goes back there when it is done.
	Origin string `json:"origin,omitempty"`
}

func (r SpeedRequest) modes() []string {
	if r.Mode == "both" {
		return []string{"single", "multi"}
	}
	return []string{r.Mode}
}

func (r SpeedRequest) validate() error {
	if len(r.NodeIDs) == 0 || len(r.NodeIDs) > speedMaxNodes || len(r.Points) == 0 || len(r.Points) > speedMaxPoints ||
		!slices.Contains([]string{"single", "multi", "both"}, r.Mode) || (r.Family != 4 && r.Family != 6) {
		return ErrSpeedRequest
	}
	seen := map[string]bool{}
	for _, p := range r.Points {
		if seen[p] || !validSpeedPoint(p) {
			return ErrSpeedRequest
		}
		seen[p] = true
	}
	return nil
}

// validSpeedPoint accepts only a known province followed by a carrier, which
// is also what makes a point safe to put on a command line.
func validSpeedPoint(p string) bool {
	for _, c := range SpeedCarriers {
		if prov, ok := strings.CutSuffix(p, c); ok && slices.Contains(SpeedProvinces, prov) {
			return true
		}
	}
	return false
}

// SpeedProgress is how far a running job has got on one machine.
type SpeedProgress struct {
	NodeID  int64  `json:"node_id"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Current string `json:"current"`
}

type speedJobRun struct {
	cancel    context.CancelFunc
	cancelled bool
	nodes     []int64
	progress  map[int64]*SpeedProgress
}

type speedState struct {
	mu   sync.Mutex
	jobs map[int64]*speedJobRun
	busy map[int64]int64
}

// SetSpeedTool gives the service the speed test client pushed to machines.
func (s *Service) SetSpeedTool(t TraceTool) { s.speedTool = t }

// SetSpeedDone is called with every job once it ends, however it ended.
func (s *Service) SetSpeedDone(fn func(ctx context.Context, jobID int64, req SpeedRequest)) {
	s.speedDone = fn
}

// InterruptSpeedJobs closes jobs a previous run of the panel left open.
func (s *Service) InterruptSpeedJobs(ctx context.Context) error {
	return s.store.InterruptSpeedJobs(ctx, time.Now().Unix())
}

// StartSpeedtest checks the request and runs it in the background.
func (s *Service) StartSpeedtest(ctx context.Context, req SpeedRequest) (int64, error) {
	if s.speedTool == nil {
		return 0, errors.New("面板未配置测速工具")
	}
	if err := req.validate(); err != nil {
		return 0, err
	}
	var nodes []*model.Node
	for _, id := range req.NodeIDs {
		n, err := s.store.NodeByID(ctx, id)
		if err != nil {
			return 0, err
		}
		if n.Status != model.StatusOnline {
			return 0, fmt.Errorf("%w：%s 不在线", ErrSpeedRequest, n.Name)
		}
		nodes = append(nodes, n)
	}
	params, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}

	s.speed.mu.Lock()
	defer s.speed.mu.Unlock()
	if s.speed.jobs == nil {
		s.speed.jobs, s.speed.busy = map[int64]*speedJobRun{}, map[int64]int64{}
	}
	for _, n := range nodes {
		if _, busy := s.speed.busy[n.ID]; busy {
			return 0, fmt.Errorf("%w（%s）", ErrSpeedBusy, n.Name)
		}
	}
	id, err := s.store.CreateSpeedJob(ctx, time.Now().Unix(), string(params), speedJobsKeep)
	if err != nil {
		return 0, err
	}
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	run := &speedJobRun{cancel: cancel, progress: map[int64]*SpeedProgress{}}
	total := len(req.Points) * len(req.modes())
	for _, n := range nodes {
		s.speed.busy[n.ID] = id
		run.nodes = append(run.nodes, n.ID)
		run.progress[n.ID] = &SpeedProgress{NodeID: n.ID, Total: total}
	}
	s.speed.jobs[id] = run
	go s.runSpeedJob(jobCtx, id, run, req, nodes)
	return id, nil
}

func (s *Service) runSpeedJob(ctx context.Context, id int64, run *speedJobRun, req SpeedRequest, nodes []*model.Node) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, speedConcurrency)
	for _, n := range nodes {
		wg.Add(1)
		go func(n *model.Node) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				s.recordSpeedRun(id, store.SpeedRun{JobID: id, NodeID: n.ID, Error: "已取消"})
				return
			}
			defer func() { <-slots }()
			s.speedNode(ctx, id, run, req, n)
		}(n)
	}
	wg.Wait()

	s.speed.mu.Lock()
	status := "done"
	if run.cancelled {
		status = "cancelled"
	}
	delete(s.speed.jobs, id)
	for _, n := range run.nodes {
		delete(s.speed.busy, n)
	}
	s.speed.mu.Unlock()
	run.cancel()
	if err := s.store.FinishSpeedJob(context.Background(), id, time.Now().Unix(), status); err != nil {
		s.log.Error("finish speed job", "job", id, "error", err)
	}
	if s.speedDone != nil {
		s.speedDone(context.Background(), id, req)
	}
}

func (s *Service) recordSpeedRun(id int64, r store.SpeedRun) {
	if err := s.store.SetSpeedRun(context.Background(), r); err != nil {
		s.log.Error("record speed run", "job", id, "node", r.NodeID, "error", err)
	}
}

func (s *Service) setSpeedProgress(run *speedJobRun, nodeID int64, done int, current string) {
	s.speed.mu.Lock()
	defer s.speed.mu.Unlock()
	if p := run.progress[nodeID]; p != nil {
		p.Done, p.Current = done, current
	}
}

// speedNode tests every point on one machine, one point and mode per run of
// the client: each run queues for a fresh key, and a key reused for the
// multi-thread passes after the single-thread ones is often refused.
func (s *Service) speedNode(ctx context.Context, id int64, run *speedJobRun, req SpeedRequest, node *model.Node) {
	rec := store.SpeedRun{JobID: id, NodeID: node.ID, StartedAt: time.Now().Unix()}
	defer func() {
		rec.FinishedAt = time.Now().Unix()
		s.recordSpeedRun(id, rec)
	}()
	if req.Family == 6 && node.IPv6Address == "" {
		rec.Error = "这台机器没有 IPv6"
		return
	}
	nodeCtx, cancel := context.WithTimeout(ctx, time.Duration(len(req.Points)*len(req.modes())*(speedRunSeconds+20)+60)*time.Second)
	defer cancel()
	client, err := s.pool.Get(nodeCtx, node)
	if err != nil {
		rec.Error = "连不上机器：" + err.Error()
		return
	}
	bin, err := ensureTool(nodeCtx, client.Client, node, s.speedTool, "taierspeedtest", "测速工具")
	if err != nil {
		rec.Error = err.Error()
		return
	}
	before, err := machineTraffic(nodeCtx, client.Client)
	if err != nil {
		rec.Error = "读取网卡流量失败：" + err.Error()
		return
	}
	defer func() {
		after, err := machineTraffic(context.Background(), client.Client)
		if err == nil && after.rx >= before.rx && after.tx >= before.tx {
			rec.RxBytes, rec.TxBytes = after.rx-before.rx, after.tx-before.tx
			rec.TrafficBytes = rec.RxBytes + rec.TxBytes
		}
	}()

	done := 0
	for _, point := range req.Points {
		for _, mode := range req.modes() {
			if nodeCtx.Err() != nil {
				if ctx.Err() != nil {
					rec.Error = "已取消"
				} else {
					rec.Error = "测速超时"
				}
				return
			}
			s.setSpeedProgress(run, node.ID, done, point)
			res, err := sshx.Run(nodeCtx, client.Client, speedScript(bin, point, mode, req.Family))
			result := store.SpeedResult{JobID: id, NodeID: node.ID, Point: point, Mode: mode}
			if err != nil {
				result.Error = "执行失败：" + err.Error()
			} else {
				egress, parsed := parseSpeed(res.Stdout, mode)
				if egress != "" {
					rec.Egress = egress
				}
				result.RTT, result.Down, result.Up, result.Retrans, result.Retried, result.Error =
					parsed.RTT, parsed.Down, parsed.Up, parsed.Retrans, parsed.Retried, parsed.Error
			}
			if err := s.store.AddSpeedResult(context.Background(), result); err != nil {
				s.log.Error("record speed result", "job", id, "error", err)
			}
			done++
			s.setSpeedProgress(run, node.ID, done, "")
		}
	}
}

// CancelSpeedtest stops a running job and the clients it started.
func (s *Service) CancelSpeedtest(ctx context.Context, id int64) error {
	s.speed.mu.Lock()
	run := s.speed.jobs[id]
	if run != nil {
		run.cancelled = true
	}
	s.speed.mu.Unlock()
	if run == nil {
		return store.ErrNotFound
	}
	run.cancel()
	for _, nodeID := range run.nodes {
		node, err := s.store.NodeByID(ctx, nodeID)
		if err != nil {
			continue
		}
		killCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if client, err := s.pool.Get(killCtx, node); err == nil {
			// The bracket keeps pkill from matching the shell running it.
			if _, err := sshx.Run(killCtx, client.Client, "pkill -f '[t]aierspeedtest-' || true"); err != nil {
				s.log.Warn("stop speed test", "node", node.Name, "error", err)
			}
		}
		cancel()
	}
	return nil
}

func (s *Service) DeleteSpeedtest(ctx context.Context, id int64) error {
	return s.store.DeleteSpeedJob(ctx, id)
}

type SpeedJobView struct {
	ID         int64               `json:"id"`
	CreatedAt  int64               `json:"created_at"`
	FinishedAt int64               `json:"finished_at"`
	Status     string              `json:"status"`
	Request    SpeedRequest        `json:"request"`
	Runs       []store.SpeedRun    `json:"runs"`
	Results    []store.SpeedResult `json:"results"`
	Progress   []SpeedProgress     `json:"progress"`
}

type SpeedOverview struct {
	Provinces []string       `json:"provinces"`
	Carriers  []string       `json:"carriers"`
	Tool      string         `json:"tool"`
	Jobs      []SpeedJobView `json:"jobs"`
}

func (s *Service) SpeedOverview(ctx context.Context) (*SpeedOverview, error) {
	jobs, runs, results, err := s.store.SpeedJobs(ctx)
	if err != nil {
		return nil, err
	}
	out := &SpeedOverview{Provinces: SpeedProvinces, Carriers: SpeedCarriers, Jobs: []SpeedJobView{}}
	if s.speedTool != nil {
		out.Tool = "taierspeedtest " + s.speedTool.Version()
	}
	s.speed.mu.Lock()
	defer s.speed.mu.Unlock()
	for _, j := range jobs {
		v := SpeedJobView{ID: j.ID, CreatedAt: j.CreatedAt, FinishedAt: j.FinishedAt, Status: j.Status,
			Runs: runs[j.ID], Results: results[j.ID], Progress: []SpeedProgress{}}
		if err := json.Unmarshal([]byte(j.Params), &v.Request); err != nil {
			return nil, fmt.Errorf("decode speed job %d: %w", j.ID, err)
		}
		if v.Runs == nil {
			v.Runs = []store.SpeedRun{}
		}
		for i := range v.Runs {
			v.Runs[i].Egress = MaskEgress(v.Runs[i].Egress)
		}
		if v.Results == nil {
			v.Results = []store.SpeedResult{}
		}
		if run := s.speed.jobs[j.ID]; run != nil {
			for _, n := range run.nodes {
				v.Progress = append(v.Progress, *run.progress[n])
			}
		}
		out.Jobs = append(out.Jobs, v)
	}
	return out, nil
}

type trafficCount struct{ rx, tx int64 }

// machineTraffic is the bytes every real interface has received and sent,
// the numbers a provider bills on; loopback and container bridges are left
// out.
func machineTraffic(ctx context.Context, client *ssh.Client) (trafficCount, error) {
	res, err := sshx.Run(ctx, client, `awk -F'[: ]+' 'NR>2 && $2 !~ /^(lo|docker|veth|br-|virbr|ifb)/ { r += $3; t += $11 } END { printf "%.0f %.0f\n", r, t }' /proc/net/dev`)
	if err != nil {
		return trafficCount{}, err
	}
	return parseTraffic(res.Stdout)
}

func parseTraffic(out string) (trafficCount, error) {
	f := strings.Fields(out)
	if len(f) != 2 {
		return trafficCount{}, fmt.Errorf("unexpected traffic reading %q", out)
	}
	rx, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return trafficCount{}, err
	}
	tx, err := strconv.ParseInt(f[1], 10, 64)
	if err != nil {
		return trafficCount{}, err
	}
	return trafficCount{rx: rx, tx: tx}, nil
}

func speedScript(bin, point, mode string, family int) string {
	return fmt.Sprintf(`E=$(mktemp)
T=""; command -v timeout >/dev/null 2>&1 && T="timeout -k 5 %d"
$T %s --json --no-image --ip %d --duration 5 --retries 1 --mode %s --points '%s' </dev/null 2>"$E"
echo "@@rc $?"
tr '\r' '\n' < "$E" | grep -v '^[[:space:]]*$' | tail -n 3 | sed 's/^/@@err /'
rm -f "$E"
`, speedRunSeconds, bin, family, mode, point)
}

type speedParsed struct {
	RTT     string
	Down    *float64
	Up      *float64
	Retrans *float64
	Retried int
	Error   string
}

type taierRow struct {
	Region      string  `json:"region"`
	RTT         string  `json:"rtt"`
	SingleUpF   float64 `json:"single_up_f"`
	SingleDownF float64 `json:"single_down_f"`
	MultiUpF    float64 `json:"multi_up_f"`
	MultiDownF  float64 `json:"multi_down_f"`
	RetransF    float64 `json:"retrans_f"`
	Retried     int     `json:"retried"`
	Err         string  `json:"error"`
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// parseSpeed reads one run of the client: the egress line it prints first,
// its JSON rows, then the exit code and the end of its stderr.
func parseSpeed(out, mode string) (egress string, p speedParsed) {
	out = ansiPattern.ReplaceAllString(out, "")
	var body, errs []string
	rc := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "@@rc "):
			rc = strings.TrimSpace(strings.TrimPrefix(line, "@@rc "))
		case strings.HasPrefix(line, "@@err "):
			errs = append(errs, strings.TrimSpace(strings.TrimPrefix(line, "@@err ")))
		case strings.Contains(line, "出口 ") && egress == "":
			egress = strings.Join(strings.Fields(strings.SplitN(line, "出口 ", 2)[1]), " ")
		default:
			body = append(body, line)
		}
	}
	text := strings.Join(body, "\n")
	start := strings.Index(text, "[")
	var rows []taierRow
	if start < 0 || json.Unmarshal([]byte(text[start:]), &rows) != nil || len(rows) == 0 {
		switch {
		case len(errs) > 0:
			p.Error = errs[len(errs)-1]
		case rc == "124" || rc == "137":
			p.Error = "测速超时"
		default:
			p.Error = "没有测到结果"
		}
		return egress, p
	}
	r := rows[0]
	p.RTT, p.Retried, p.Error = r.RTT, r.Retried, r.Err
	down, up := r.SingleDownF, r.SingleUpF
	if mode == "multi" {
		down, up = r.MultiDownF, r.MultiUpF
	}
	p.Down, p.Up = &down, &up
	if r.RetransF >= 0 {
		p.Retrans = &r.RetransF
	}
	return egress, p
}

// MaskEgress hides most of the machine's address in "<ip> <where>": the
// page and the bot show where a machine leaves from, not how to reach it.
func MaskEgress(egress string) string {
	ip, where, _ := strings.Cut(egress, " ")
	addr := net.ParseIP(ip)
	switch {
	case addr == nil:
		return egress
	case addr.To4() != nil:
		parts := strings.Split(ip, ".")
		ip = parts[0] + "." + parts[1] + ".*.*"
	default:
		parts := strings.Split(ip, ":")
		ip = parts[0] + ":" + parts[1] + ":****"
	}
	return strings.TrimSpace(ip + " " + where)
}
