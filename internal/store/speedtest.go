package store

import (
	"context"
	"database/sql"
	"fmt"
)

// SpeedJob is one speed test the operator started. Params is the request as
// JSON; Status is running, done or cancelled.
type SpeedJob struct {
	ID         int64
	CreatedAt  int64
	FinishedAt int64
	Status     string
	Params     string
}

// SpeedRun is one machine's part of a job.
type SpeedRun struct {
	JobID        int64  `json:"-"`
	NodeID       int64  `json:"node_id"`
	Egress       string `json:"egress"`
	TrafficBytes int64  `json:"traffic_bytes"`
	RxBytes      int64  `json:"rx_bytes"`
	TxBytes      int64  `json:"tx_bytes"`
	StartedAt    int64  `json:"started_at"`
	FinishedAt   int64  `json:"finished_at"`
	Error        string `json:"error,omitempty"`
}

// SpeedResult is one test point in one mode, in Mbps. Down is toward the
// machine, up toward China. Retrans is the percentage of segments the
// machine resent while uploading, nil where it could not be read.
type SpeedResult struct {
	JobID   int64    `json:"-"`
	NodeID  int64    `json:"node_id"`
	Point   string   `json:"point"`
	Mode    string   `json:"mode"`
	Family  int      `json:"family"`
	RTT     string   `json:"rtt"`
	Down    *float64 `json:"down"`
	Up      *float64 `json:"up"`
	Retrans *float64 `json:"retrans"`
	Retried int      `json:"retried"`
	Error   string   `json:"error,omitempty"`
}

// CreateSpeedJob records a new running job and keeps only the newest keep.
func (s *Store) CreateSpeedJob(ctx context.Context, createdAt int64, params string, keep int) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO speed_jobs (created_at, status, params) VALUES (?, 'running', ?)`, createdAt, params)
	if err != nil {
		return 0, fmt.Errorf("create speed job: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM speed_jobs WHERE id NOT IN (SELECT id FROM speed_jobs ORDER BY id DESC LIMIT ?)`, keep); err != nil {
		return 0, fmt.Errorf("prune speed jobs: %w", err)
	}
	return id, nil
}

func (s *Store) FinishSpeedJob(ctx context.Context, id, finishedAt int64, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE speed_jobs SET status = ?, finished_at = ? WHERE id = ?`, status, finishedAt, id)
	if err != nil {
		return fmt.Errorf("finish speed job: %w", err)
	}
	return nil
}

// InterruptSpeedJobs marks jobs left running by a panel that stopped.
func (s *Store) InterruptSpeedJobs(ctx context.Context, finishedAt int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE speed_jobs SET status = 'interrupted', finished_at = ? WHERE status = 'running'`, finishedAt)
	if err != nil {
		return fmt.Errorf("interrupt speed jobs: %w", err)
	}
	return nil
}

// DeleteSpeedJob removes a finished job; a running one is left alone.
func (s *Store) DeleteSpeedJob(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM speed_jobs WHERE id = ? AND status != 'running'`, id)
	if err != nil {
		return fmt.Errorf("delete speed job: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetSpeedRun(ctx context.Context, r SpeedRun) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO speed_runs (job_id, node_id, egress, traffic_bytes, rx_bytes, tx_bytes, started_at, finished_at, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(job_id, node_id) DO UPDATE SET
			egress = excluded.egress, traffic_bytes = excluded.traffic_bytes, rx_bytes = excluded.rx_bytes,
			tx_bytes = excluded.tx_bytes, started_at = excluded.started_at, finished_at = excluded.finished_at,
			error = excluded.error`,
		r.JobID, r.NodeID, r.Egress, r.TrafficBytes, r.RxBytes, r.TxBytes, r.StartedAt, r.FinishedAt, r.Error)
	if err != nil {
		return fmt.Errorf("record speed run: %w", err)
	}
	return nil
}

func (s *Store) AddSpeedResult(ctx context.Context, r SpeedResult) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO speed_results (job_id, node_id, point, mode, family, rtt, down, up, retrans, retried, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.JobID, r.NodeID, r.Point, r.Mode, r.Family, r.RTT, r.Down, r.Up, r.Retrans, r.Retried, r.Error)
	if err != nil {
		return fmt.Errorf("record speed result: %w", err)
	}
	return nil
}

// SpeedJobs returns every kept job, newest first, with its runs and results.
func (s *Store) SpeedJobs(ctx context.Context) ([]SpeedJob, map[int64][]SpeedRun, map[int64][]SpeedResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, created_at, finished_at, status, params FROM speed_jobs ORDER BY id DESC`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("query speed jobs: %w", err)
	}
	defer rows.Close()
	var jobs []SpeedJob
	for rows.Next() {
		var j SpeedJob
		if err := rows.Scan(&j.ID, &j.CreatedAt, &j.FinishedAt, &j.Status, &j.Params); err != nil {
			return nil, nil, nil, fmt.Errorf("scan speed job: %w", err)
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}

	runs := make(map[int64][]SpeedRun)
	runRows, err := s.db.QueryContext(ctx,
		`SELECT job_id, node_id, egress, traffic_bytes, rx_bytes, tx_bytes, started_at, finished_at, error
		 FROM speed_runs ORDER BY job_id, node_id`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("query speed runs: %w", err)
	}
	defer runRows.Close()
	for runRows.Next() {
		var r SpeedRun
		if err := runRows.Scan(&r.JobID, &r.NodeID, &r.Egress, &r.TrafficBytes, &r.RxBytes, &r.TxBytes, &r.StartedAt, &r.FinishedAt, &r.Error); err != nil {
			return nil, nil, nil, fmt.Errorf("scan speed run: %w", err)
		}
		runs[r.JobID] = append(runs[r.JobID], r)
	}
	if err := runRows.Err(); err != nil {
		return nil, nil, nil, err
	}

	results := make(map[int64][]SpeedResult)
	resRows, err := s.db.QueryContext(ctx, `
		SELECT job_id, node_id, point, mode, family, rtt, down, up, retrans, retried, error
		FROM speed_results ORDER BY job_id, node_id, rowid`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("query speed results: %w", err)
	}
	defer resRows.Close()
	for resRows.Next() {
		var r SpeedResult
		var down, up, retrans sql.NullFloat64
		if err := resRows.Scan(&r.JobID, &r.NodeID, &r.Point, &r.Mode, &r.Family, &r.RTT, &down, &up, &retrans, &r.Retried, &r.Error); err != nil {
			return nil, nil, nil, fmt.Errorf("scan speed result: %w", err)
		}
		r.Down, r.Up, r.Retrans = nullFloat(down), nullFloat(up), nullFloat(retrans)
		results[r.JobID] = append(results[r.JobID], r)
	}
	return jobs, runs, results, resRows.Err()
}

func nullFloat(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	return &v.Float64
}

// QualityResult is one return-quality item for one point: Line for the
// route and UDP items, MS and Loss (percent) for the route and large items.
type QualityResult struct {
	JobID  int64  `json:"-"`
	NodeID int64  `json:"node_id"`
	Point  string `json:"point"`
	Item   string `json:"item"`
	Family int    `json:"family"`
	Line   string `json:"line,omitempty"`
	MS     *int   `json:"ms"`
	Loss   *int   `json:"loss"`
	Error  string `json:"error,omitempty"`
}

func (s *Store) AddQualityResult(ctx context.Context, r QualityResult) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO quality_results (job_id, node_id, point, item, family, line, ms, loss, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.JobID, r.NodeID, r.Point, r.Item, r.Family, r.Line, r.MS, r.Loss, r.Error)
	if err != nil {
		return fmt.Errorf("record quality result: %w", err)
	}
	return nil
}

// QualityResults returns every kept job's return-quality results.
func (s *Store) QualityResults(ctx context.Context) (map[int64][]QualityResult, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT job_id, node_id, point, item, family, line, ms, loss, error FROM quality_results ORDER BY job_id, node_id, rowid`)
	if err != nil {
		return nil, fmt.Errorf("query quality results: %w", err)
	}
	defer rows.Close()
	out := make(map[int64][]QualityResult)
	for rows.Next() {
		var r QualityResult
		var ms, loss sql.NullInt64
		if err := rows.Scan(&r.JobID, &r.NodeID, &r.Point, &r.Item, &r.Family, &r.Line, &ms, &loss, &r.Error); err != nil {
			return nil, fmt.Errorf("scan quality result: %w", err)
		}
		if ms.Valid {
			v := int(ms.Int64)
			r.MS = &v
		}
		if loss.Valid {
			v := int(loss.Int64)
			r.Loss = &v
		}
		out[r.JobID] = append(out[r.JobID], r)
	}
	return out, rows.Err()
}

// PruneSpeedJobs removes finished jobs created before the given time.
func (s *Store) PruneSpeedJobs(ctx context.Context, before int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM speed_jobs WHERE created_at < ? AND status != 'running'`, before); err != nil {
		return fmt.Errorf("prune speed jobs: %w", err)
	}
	return nil
}
