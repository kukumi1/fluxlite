package probe

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/kukumi1/fluxlite/internal/store"
)

// ThemeSettingsPrefix keys a theme's saved settings, followed by its name.
const ThemeSettingsPrefix = "theme_settings."

// OfflineNotifySetting lists, as JSON, the machines whose going offline is
// worth a notice.
const OfflineNotifySetting = "offline_notify"

// The site settings brought over from Komari; values there are JSON.
var komariSettings = []string{"sitename", "description", "custom_head", "custom_body", "theme", DiscoveryKeySetting}

// ImportKomari reads a Komari server's machines, latency checks, site
// settings, the active theme's settings and the last week of history.
// Both databases are opened read-only; Komari is left as it was.
func ImportKomari(ctx context.Context, komariDB, metricsDB string, now time.Time) (store.ProbeImport, error) {
	out := store.ProbeImport{Settings: map[string]string{}, Samples: map[int64][]store.ProbeSample{}, Pings: map[int64][]store.ProbePing{}}
	kdb, err := openReadOnly(ctx, komariDB)
	if err != nil {
		return out, err
	}
	defer kdb.Close()
	if out.Clients, err = komariClients(ctx, kdb); err != nil {
		return out, err
	}
	known := map[string]bool{}
	for _, c := range out.Clients {
		known[c.UUID] = true
	}
	if out.Tasks, err = komariTasks(ctx, kdb, out.Clients); err != nil {
		return out, err
	}
	if err := komariSiteSettings(ctx, kdb, out.Settings); err != nil {
		return out, err
	}

	mdb, err := openReadOnly(ctx, metricsDB)
	if err != nil {
		return out, err
	}
	defer mdb.Close()
	if err := komariHistory(ctx, mdb, known, now, &out); err != nil {
		return out, err
	}
	return out, nil
}

func openReadOnly(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return db, nil
}

func komariClients(ctx context.Context, db *sql.DB) ([]store.ProbeClient, error) {
	rows, err := db.QueryContext(ctx, `SELECT uuid, token, coalesce(name, ''), coalesce(hidden, 0), coalesce(weight, 0),
		coalesce(CAST(strftime('%s', created_at) AS INTEGER), 0), coalesce(CAST(strftime('%s', updated_at) AS INTEGER), 0),
		json_object('cpu_name', cpu_name, 'virtualization', virtualization, 'arch', arch, 'cpu_cores', cpu_cores,
			'cpu_physical_cores', cpu_physical_cores, 'os', os, 'kernel_version', kernel_version, 'gpu_name', gpu_name,
			'ipv4', ipv4, 'ipv6', ipv6, 'mem_total', mem_total, 'swap_total', swap_total, 'disk_total', disk_total, 'version', version),
		json_object('region', region, 'remark', remark, 'public_remark', public_remark, 'price', price, 'billing_cycle', billing_cycle,
			'auto_renewal', CASE WHEN auto_renewal THEN json('true') ELSE json('false') END, 'currency', currency,
			'expired_at', expired_at, 'group', "group", 'tags', tags, 'traffic_limit', traffic_limit,
			'traffic_limit_type', traffic_limit_type)
		FROM clients`)
	if err != nil {
		return nil, fmt.Errorf("read Komari machines: %w", err)
	}
	defer rows.Close()
	var out []store.ProbeClient
	for rows.Next() {
		var c store.ProbeClient
		var info, display string
		if err := rows.Scan(&c.UUID, &c.Token, &c.Name, &c.Hidden, &c.Weight, &c.CreatedAt, &c.UpdatedAt, &info, &display); err != nil {
			return nil, fmt.Errorf("read Komari machine: %w", err)
		}
		c.Info, c.Display = json.RawMessage(info), json.RawMessage(display)
		out = append(out, c)
	}
	return out, rows.Err()
}

func komariTasks(ctx context.Context, db *sql.DB, clients []store.ProbeClient) ([]store.ProbeTask, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, type, target, interval, coalesce(weight, 0), coalesce(clients, '[]'), coalesce(all_clients, 0) FROM ping_tasks`)
	if err != nil {
		return nil, fmt.Errorf("read Komari latency checks: %w", err)
	}
	defer rows.Close()
	var out []store.ProbeTask
	for rows.Next() {
		var t store.ProbeTask
		var list string
		var all bool
		if err := rows.Scan(&t.ID, &t.Name, &t.Type, &t.Target, &t.Interval, &t.Weight, &list, &all); err != nil {
			return nil, fmt.Errorf("read Komari latency check: %w", err)
		}
		if list == "" || list == "null" {
			list = "[]"
		}
		if err := json.Unmarshal([]byte(list), &t.Clients); err != nil {
			return nil, fmt.Errorf("read machines of latency check %s: %w", t.Name, err)
		}
		if all {
			t.Clients = t.Clients[:0]
			for _, c := range clients {
				t.Clients = append(t.Clients, c.UUID)
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func komariSiteSettings(ctx context.Context, db *sql.DB, settings map[string]string) error {
	for _, key := range komariSettings {
		var raw string
		err := db.QueryRowContext(ctx, `SELECT value FROM configs WHERE key = ?`, key).Scan(&raw)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return fmt.Errorf("read Komari setting %s: %w", key, err)
		}
		var v string
		if json.Unmarshal([]byte(raw), &v) != nil {
			v = raw
		}
		settings[key] = v
	}
	theme := settings["theme"]
	var data sql.NullString
	err := db.QueryRowContext(ctx, `SELECT data FROM theme_configurations WHERE short = ?`, theme).Scan(&data)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read Komari theme settings: %w", err)
	}
	if data.Valid {
		settings[ThemeSettingsPrefix+theme] = data.String
	}
	var notify []string
	rows, err := db.QueryContext(ctx, `SELECT client FROM offline_notifications WHERE enable`)
	if err != nil {
		return fmt.Errorf("read Komari offline notices: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return err
		}
		notify = append(notify, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	b, err := json.Marshal(notify)
	if err != nil {
		return err
	}
	settings[OfflineNotifySetting] = string(b)
	return nil
}

type rollup struct {
	count    int64
	sum      float64
	min, max float64
}

// komariHistory turns Komari's rollups into the probe's buckets. A lost
// latency probe is stored there as -1 and counted in the sum, so the sum of
// answers is its sum plus the number lost.
func komariHistory(ctx context.Context, db *sql.DB, known map[string]bool, now time.Time, out *store.ProbeImport) error {
	type sampleKey struct {
		client      string
		res, bucket int64
	}
	type pingKey struct {
		task        int64
		client      string
		res, bucket int64
	}
	samples := map[sampleKey]map[string]rollup{}
	latency, loss := map[pingKey]rollup{}, map[pingKey]rollup{}

	rows, err := db.QueryContext(ctx, `SELECT s.metric_name, s.entity_id, s.tags, res.resolution_milli / 1000, r.bucket_milli / 1000,
			r.count, r.sum, r.min_val, r.max_val
		FROM metric_rollups r JOIN metric_series s ON s.id = r.series_id JOIN metric_resolutions res ON res.id = r.resolution_id
		WHERE res.resolution_milli IN (60000, 300000, 3600000) AND r.bucket_milli >= ? AND r.count > 0`,
		now.Add(-7*24*time.Hour).UnixMilli())
	if err != nil {
		return fmt.Errorf("read Komari history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var metric, client, tags string
		var res, bucket int64
		var r rollup
		if err := rows.Scan(&metric, &client, &tags, &res, &bucket, &r.count, &r.sum, &r.min, &r.max); err != nil {
			return fmt.Errorf("read Komari history: %w", err)
		}
		if !known[client] {
			continue
		}
		if metric == "ping.latency_ms" || metric == "ping.loss" {
			var t struct {
				TaskID string `json:"task_id"`
			}
			if json.Unmarshal([]byte(tags), &t) != nil {
				continue
			}
			task, err := strconv.ParseInt(t.TaskID, 10, 64)
			if err != nil {
				continue
			}
			k := pingKey{task, client, res, bucket}
			if metric == "ping.loss" {
				loss[k] = r
			} else {
				latency[k] = r
			}
			continue
		}
		k := sampleKey{client, res, bucket}
		if samples[k] == nil {
			samples[k] = map[string]rollup{}
		}
		samples[k][metric] = r
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for k, metrics := range samples {
		cpu, ok := metrics["cpu.usage"]
		if !ok {
			continue
		}
		n := cpu.count
		total := func(name string) float64 {
			r, ok := metrics[name]
			if !ok {
				return 0
			}
			return r.sum / float64(r.count) * float64(n)
		}
		p := store.ProbeSample{Client: k.client, Bucket: k.bucket, N: n, CPU: cpu.sum,
			RAM: total("memory.used"), Swap: total("swap.used"), Load1: total("load.average"), Disk: total("disk.used"),
			NetIn: total("net.in.rate"), NetOut: total("net.out.rate"), Process: total("process.count"),
			TCP: total("connections.tcp"), UDP: total("connections.udp"),
			TotalUp: int64(metrics["net.total.up"].max), TotalDown: int64(metrics["net.total.down"].max)}
		p.Load5, p.Load15 = p.Load1, p.Load1
		out.Samples[k.res] = append(out.Samples[k.res], p)
	}
	for k, l := range latency {
		lost := int64(math.Round(loss[k].sum))
		p := store.ProbePing{Task: k.task, Client: k.client, Bucket: k.bucket, Sent: l.count, Lost: lost, Sum: l.sum + float64(lost)}
		if lost < l.count {
			if l.min >= 0 {
				lo := l.min
				p.Min = &lo
			}
			if l.max >= 0 {
				hi := l.max
				p.Max = &hi
			}
		} else {
			p.Sum = 0
		}
		out.Pings[k.res] = append(out.Pings[k.res], p)
	}
	return nil
}
