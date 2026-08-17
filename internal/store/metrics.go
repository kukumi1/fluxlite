package store

import (
	"context"
	"fmt"

	"github.com/kukumi1/fluxlite/internal/model"
)

// UpsertNodeMetrics replaces a node's snapshot with the one just collected.
//
// Only the latest reading is kept. A history would need a retention policy and
// a chart to justify itself; the question this answers is "what is that machine
// doing right now", which one row answers completely.
func (s *Store) UpsertNodeMetrics(ctx context.Context, m *model.NodeMetrics) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO node_metrics (node_id, cpu_percent, cores, cpu_model,
			mem_total, mem_used, swap_total, swap_used, disk_total, disk_used,
			load1, load5, load15, uptime_sec, kernel,
			net_rx_bytes, net_tx_bytes, net_rx_rate, net_tx_rate,
			mem_source, container, collected_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(node_id) DO UPDATE SET
			cpu_percent = excluded.cpu_percent, cores = excluded.cores,
			cpu_model = excluded.cpu_model, mem_total = excluded.mem_total,
			mem_used = excluded.mem_used, swap_total = excluded.swap_total,
			swap_used = excluded.swap_used, disk_total = excluded.disk_total,
			disk_used = excluded.disk_used, load1 = excluded.load1,
			load5 = excluded.load5, load15 = excluded.load15,
			uptime_sec = excluded.uptime_sec, kernel = excluded.kernel,
			net_rx_bytes = excluded.net_rx_bytes, net_tx_bytes = excluded.net_tx_bytes,
			net_rx_rate = excluded.net_rx_rate, net_tx_rate = excluded.net_tx_rate,
			mem_source = excluded.mem_source, container = excluded.container,
			collected_at = excluded.collected_at`,
		m.NodeID, m.CPUPercent, m.Cores, m.CPUModel,
		m.MemTotal, m.MemUsed, m.SwapTotal, m.SwapUsed, m.DiskTotal, m.DiskUsed,
		m.Load1, m.Load5, m.Load15, m.UptimeSec, m.Kernel,
		m.NetRxBytes, m.NetTxBytes, m.NetRxRate, m.NetTxRate,
		string(m.MemSource), m.Container, m.CollectedAt)
	if err != nil {
		return fmt.Errorf("upsert node metrics: %w", err)
	}
	return nil
}

// ListNodeMetrics returns the latest snapshot per node, keyed by node id.
// Nodes never collected are simply absent, which the caller shows as unknown.
func (s *Store) ListNodeMetrics(ctx context.Context) (map[int64]*model.NodeMetrics, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT node_id, cpu_percent, cores, cpu_model,
			mem_total, mem_used, swap_total, swap_used, disk_total, disk_used,
			load1, load5, load15, uptime_sec, kernel,
			net_rx_bytes, net_tx_bytes, net_rx_rate, net_tx_rate,
			mem_source, container, collected_at
		FROM node_metrics`)
	if err != nil {
		return nil, fmt.Errorf("query node metrics: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]*model.NodeMetrics)
	for rows.Next() {
		m := &model.NodeMetrics{}
		var source string
		if err := rows.Scan(&m.NodeID, &m.CPUPercent, &m.Cores, &m.CPUModel,
			&m.MemTotal, &m.MemUsed, &m.SwapTotal, &m.SwapUsed, &m.DiskTotal, &m.DiskUsed,
			&m.Load1, &m.Load5, &m.Load15, &m.UptimeSec, &m.Kernel,
			&m.NetRxBytes, &m.NetTxBytes, &m.NetRxRate, &m.NetTxRate,
			&source, &m.Container, &m.CollectedAt); err != nil {
			return nil, fmt.Errorf("scan node metrics: %w", err)
		}
		m.MemSource = model.MemSource(source)
		out[m.NodeID] = m
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node metrics: %w", err)
	}
	return out, nil
}
