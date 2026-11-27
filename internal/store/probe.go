package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ProbeClient is a machine reporting through a Komari agent. Info is what
// the agent says about its hardware; Display holds the fields only the
// public page shows, such as price and expiry, kept as Komari named them.
type ProbeClient struct {
	UUID      string          `json:"uuid"`
	Token     string          `json:"-"`
	Name      string          `json:"name"`
	Info      json.RawMessage `json:"info"`
	Display   json.RawMessage `json:"display"`
	Hidden    bool            `json:"hidden"`
	Weight    int             `json:"weight"`
	NodeID    *int64          `json:"node_id"`
	CreatedAt int64           `json:"created_at"`
	UpdatedAt int64           `json:"updated_at"`
}

// ProbeTask is a latency check the panel has the agents run.
type ProbeTask struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Target   string   `json:"target"`
	Interval int      `json:"interval"`
	Weight   int      `json:"weight"`
	Clients  []string `json:"clients"`
}

// ProbeSample is a bucket of reports: N of them, each metric summed, and the
// highest of the agent's running traffic totals.
type ProbeSample struct {
	Client    string  `json:"client"`
	Bucket    int64   `json:"bucket"`
	N         int64   `json:"n"`
	CPU       float64 `json:"cpu"`
	RAM       float64 `json:"ram"`
	Swap      float64 `json:"swap"`
	Load1     float64 `json:"load1"`
	Load5     float64 `json:"load5"`
	Load15    float64 `json:"load15"`
	Disk      float64 `json:"disk"`
	NetIn     float64 `json:"net_in"`
	NetOut    float64 `json:"net_out"`
	Process   float64 `json:"process"`
	TCP       float64 `json:"tcp"`
	UDP       float64 `json:"udp"`
	TotalUp   int64   `json:"total_up"`
	TotalDown int64   `json:"total_down"`
}

// ProbePing is a bucket of one task's results on one machine: Sent probes,
// Lost of them unanswered, and the sum, lowest and highest of the answers.
type ProbePing struct {
	Task   int64    `json:"task"`
	Client string   `json:"client"`
	Bucket int64    `json:"bucket"`
	Sent   int64    `json:"sent"`
	Lost   int64    `json:"lost"`
	Sum    float64  `json:"sum"`
	Min    *float64 `json:"min"`
	Max    *float64 `json:"max"`
}

// Probe history is kept per minute for a day, and per five minutes and per
// hour for a week.
const (
	ProbeMinute = 60
	ProbeFive   = 300
	ProbeHour   = 3600
)

var ProbeResolutions = []int64{ProbeMinute, ProbeFive, ProbeHour}

func probeKeep(res int64) time.Duration {
	if res == ProbeMinute {
		return 24 * time.Hour
	}
	return 7 * 24 * time.Hour
}

const probeClientColumns = `uuid, token, name, info, display, hidden, weight, node_id, created_at, updated_at`

func scanProbeClient(row interface{ Scan(...any) error }) (*ProbeClient, error) {
	var c ProbeClient
	var info, display string
	var nodeID sql.NullInt64
	if err := row.Scan(&c.UUID, &c.Token, &c.Name, &info, &display, &c.Hidden, &c.Weight, &nodeID, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Info, c.Display = json.RawMessage(info), json.RawMessage(display)
	if nodeID.Valid {
		c.NodeID = &nodeID.Int64
	}
	return &c, nil
}

// ProbeClients lists every machine, heaviest weight first as Komari orders them.
func (s *Store) ProbeClients(ctx context.Context) ([]ProbeClient, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+probeClientColumns+` FROM probe_clients ORDER BY weight, name`)
	if err != nil {
		return nil, fmt.Errorf("query probe clients: %w", err)
	}
	defer rows.Close()
	out := []ProbeClient{}
	for rows.Next() {
		c, err := scanProbeClient(rows)
		if err != nil {
			return nil, fmt.Errorf("scan probe client: %w", err)
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// ProbeClientByToken finds the machine an agent's token belongs to.
func (s *Store) ProbeClientByToken(ctx context.Context, token string) (*ProbeClient, error) {
	c, err := scanProbeClient(s.db.QueryRowContext(ctx, `SELECT `+probeClientColumns+` FROM probe_clients WHERE token = ?`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find probe client: %w", err)
	}
	return c, nil
}

// CreateProbeClient adds a machine, as an agent registering itself does.
func (s *Store) CreateProbeClient(ctx context.Context, c ProbeClient) error {
	now := time.Now().Unix()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO probe_clients (`+probeClientColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.UUID, c.Token, c.Name, rawOr(c.Info), rawOr(c.Display), c.Hidden, c.Weight, c.NodeID, now, now); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return ErrConflict
		}
		return fmt.Errorf("create probe client: %w", err)
	}
	return nil
}

// SetProbeInfo records what an agent says about its machine.
func (s *Store) SetProbeInfo(ctx context.Context, uuid string, info json.RawMessage) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE probe_clients SET info = ?, updated_at = ? WHERE uuid = ?`, string(info), time.Now().Unix(), uuid); err != nil {
		return fmt.Errorf("set probe info: %w", err)
	}
	return nil
}

func rawOr(r json.RawMessage) string {
	if len(r) == 0 {
		return "{}"
	}
	return string(r)
}

// ProbeTasks lists the latency checks.
func (s *Store) ProbeTasks(ctx context.Context) ([]ProbeTask, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, target, interval, weight, clients FROM probe_ping_tasks ORDER BY weight, id`)
	if err != nil {
		return nil, fmt.Errorf("query probe tasks: %w", err)
	}
	defer rows.Close()
	out := []ProbeTask{}
	for rows.Next() {
		var t ProbeTask
		var clients string
		if err := rows.Scan(&t.ID, &t.Name, &t.Type, &t.Target, &t.Interval, &t.Weight, &clients); err != nil {
			return nil, fmt.Errorf("scan probe task: %w", err)
		}
		if err := json.Unmarshal([]byte(clients), &t.Clients); err != nil {
			return nil, fmt.Errorf("decode probe task %d machines: %w", t.ID, err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

const probeSumColumns = `n, cpu, ram, swap, load1, load5, load15, disk, net_in, net_out, process, tcp, udp, total_up, total_down`

func probeSampleArgs(p ProbeSample) []any {
	return []any{p.N, p.CPU, p.RAM, p.Swap, p.Load1, p.Load5, p.Load15, p.Disk, p.NetIn, p.NetOut, p.Process, p.TCP, p.UDP, p.TotalUp, p.TotalDown}
}

// AddProbeSamples adds minute buckets into every resolution's bucket that
// holds them.
func (s *Store) AddProbeSamples(ctx context.Context, samples []ProbeSample) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO probe_metrics (client, res, bucket, `+probeSumColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (client, res, bucket) DO UPDATE SET
				n = n + excluded.n, cpu = cpu + excluded.cpu, ram = ram + excluded.ram, swap = swap + excluded.swap,
				load1 = load1 + excluded.load1, load5 = load5 + excluded.load5, load15 = load15 + excluded.load15,
				disk = disk + excluded.disk, net_in = net_in + excluded.net_in, net_out = net_out + excluded.net_out,
				process = process + excluded.process, tcp = tcp + excluded.tcp, udp = udp + excluded.udp,
				total_up = max(total_up, excluded.total_up), total_down = max(total_down, excluded.total_down)`)
		if err != nil {
			return fmt.Errorf("prepare probe samples: %w", err)
		}
		defer stmt.Close()
		for _, p := range samples {
			for _, res := range ProbeResolutions {
				args := append([]any{p.Client, res, p.Bucket - p.Bucket%res}, probeSampleArgs(p)...)
				if _, err := stmt.ExecContext(ctx, args...); err != nil {
					return fmt.Errorf("add probe sample: %w", err)
				}
			}
		}
		return nil
	})
}

// ProbeSamples reads one resolution's buckets from a time on, for one
// machine or, with client empty, every machine.
func (s *Store) ProbeSamples(ctx context.Context, client string, res, from int64) ([]ProbeSample, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT client, bucket, `+probeSumColumns+` FROM probe_metrics
		WHERE res = ? AND bucket >= ? AND (? = '' OR client = ?) ORDER BY client, bucket`, res, from, client, client)
	if err != nil {
		return nil, fmt.Errorf("query probe samples: %w", err)
	}
	defer rows.Close()
	out := []ProbeSample{}
	for rows.Next() {
		var p ProbeSample
		if err := rows.Scan(&p.Client, &p.Bucket, &p.N, &p.CPU, &p.RAM, &p.Swap, &p.Load1, &p.Load5, &p.Load15, &p.Disk,
			&p.NetIn, &p.NetOut, &p.Process, &p.TCP, &p.UDP, &p.TotalUp, &p.TotalDown); err != nil {
			return nil, fmt.Errorf("scan probe sample: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AddProbePings adds minute buckets of latency results into every
// resolution's bucket that holds them.
func (s *Store) AddProbePings(ctx context.Context, pings []ProbePing) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO probe_ping (task, client, res, bucket, sent, lost, sum, min, max)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (task, client, res, bucket) DO UPDATE SET
				sent = sent + excluded.sent, lost = lost + excluded.lost, sum = sum + excluded.sum,
				min = CASE WHEN min IS NULL THEN excluded.min WHEN excluded.min IS NULL THEN min ELSE min(min, excluded.min) END,
				max = CASE WHEN max IS NULL THEN excluded.max WHEN excluded.max IS NULL THEN max ELSE max(max, excluded.max) END`)
		if err != nil {
			return fmt.Errorf("prepare probe pings: %w", err)
		}
		defer stmt.Close()
		for _, p := range pings {
			for _, res := range ProbeResolutions {
				if _, err := stmt.ExecContext(ctx, p.Task, p.Client, res, p.Bucket-p.Bucket%res, p.Sent, p.Lost, p.Sum, p.Min, p.Max); err != nil {
					return fmt.Errorf("add probe ping: %w", err)
				}
			}
		}
		return nil
	})
}

// ProbePings reads one resolution's latency buckets from a time on; an
// empty client or a zero task matches all.
func (s *Store) ProbePings(ctx context.Context, client string, task, res, from int64) ([]ProbePing, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT task, client, bucket, sent, lost, sum, min, max FROM probe_ping
		WHERE res = ? AND bucket >= ? AND (? = '' OR client = ?) AND (? = 0 OR task = ?) ORDER BY task, client, bucket`,
		res, from, client, client, task, task)
	if err != nil {
		return nil, fmt.Errorf("query probe pings: %w", err)
	}
	defer rows.Close()
	out := []ProbePing{}
	for rows.Next() {
		var p ProbePing
		var lo, hi sql.NullFloat64
		if err := rows.Scan(&p.Task, &p.Client, &p.Bucket, &p.Sent, &p.Lost, &p.Sum, &lo, &hi); err != nil {
			return nil, fmt.Errorf("scan probe ping: %w", err)
		}
		if lo.Valid {
			p.Min = &lo.Float64
		}
		if hi.Valid {
			p.Max = &hi.Float64
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PruneProbe drops buckets past their resolution's keep.
func (s *Store) PruneProbe(ctx context.Context, now time.Time) error {
	for _, res := range ProbeResolutions {
		before := now.Add(-probeKeep(res)).Unix()
		for _, table := range []string{"probe_metrics", "probe_ping"} {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM `+table+` WHERE res = ? AND bucket < ?`, res, before); err != nil {
				return fmt.Errorf("prune %s: %w", table, err)
			}
		}
	}
	return nil
}

// ProbeSetting reads a probe setting, empty when unset.
func (s *Store) ProbeSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM probe_settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read probe setting %s: %w", key, err)
	}
	return v, nil
}

func (s *Store) SetProbeSetting(ctx context.Context, key, value string) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO probe_settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		return fmt.Errorf("save probe setting %s: %w", key, err)
	}
	return nil
}

// ProbeImport is everything brought over from Komari at once.
type ProbeImport struct {
	Clients  []ProbeClient
	Tasks    []ProbeTask
	Settings map[string]string
	Samples  map[int64][]ProbeSample // by resolution, already bucketed
	Pings    map[int64][]ProbePing
}

// ReplaceProbe swaps the probe's machines, tasks, settings and history for
// an import's, in one transaction so a failed import leaves the old intact.
func (s *Store) ReplaceProbe(ctx context.Context, in ProbeImport) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"probe_metrics", "probe_ping", "probe_ping_tasks", "probe_clients"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
				return fmt.Errorf("clear %s: %w", table, err)
			}
		}
		for _, c := range in.Clients {
			if _, err := tx.ExecContext(ctx, `INSERT INTO probe_clients (`+probeClientColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				c.UUID, c.Token, c.Name, rawOr(c.Info), rawOr(c.Display), c.Hidden, c.Weight, c.NodeID, c.CreatedAt, c.UpdatedAt); err != nil {
				return fmt.Errorf("import probe client %s: %w", c.Name, err)
			}
		}
		for _, t := range in.Tasks {
			clients, err := json.Marshal(t.Clients)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO probe_ping_tasks (id, name, type, target, interval, weight, clients) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				t.ID, t.Name, t.Type, t.Target, t.Interval, t.Weight, string(clients)); err != nil {
				return fmt.Errorf("import probe task %s: %w", t.Name, err)
			}
		}
		for k, v := range in.Settings {
			if _, err := tx.ExecContext(ctx, `INSERT INTO probe_settings (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
				return fmt.Errorf("import probe setting %s: %w", k, err)
			}
		}
		for res, samples := range in.Samples {
			for _, p := range samples {
				args := append([]any{p.Client, res, p.Bucket}, probeSampleArgs(p)...)
				if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO probe_metrics (client, res, bucket, `+probeSumColumns+`)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...); err != nil {
					return fmt.Errorf("import probe sample: %w", err)
				}
			}
		}
		for res, pings := range in.Pings {
			for _, p := range pings {
				if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO probe_ping (task, client, res, bucket, sent, lost, sum, min, max)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.Task, p.Client, res, p.Bucket, p.Sent, p.Lost, p.Sum, p.Min, p.Max); err != nil {
					return fmt.Errorf("import probe ping: %w", err)
				}
			}
		}
		return nil
	})
}
