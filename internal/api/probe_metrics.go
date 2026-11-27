package api

import (
	"context"
	"net/http"
	"time"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/probe"
)

// nodeMetrics is each machine's latest readings: the agent's, live, for a
// machine whose probe agent is reporting, otherwise the panel's SSH sample.
func (s *Server) nodeMetrics(ctx context.Context) (map[int64]*model.NodeMetrics, error) {
	metrics, err := s.svc.NodeMetrics(ctx)
	if err != nil || s.probe == nil {
		return metrics, err
	}
	clients, err := s.svc.Store().ProbeClients(ctx)
	if err != nil {
		s.log.Error("list probe machines for live readings", "error", err)
		return metrics, nil
	}
	now := time.Now()
	live := s.probe.Snapshot(now)
	for _, c := range clients {
		if c.NodeID == nil {
			continue
		}
		st := live[c.UUID]
		if !st.Online || st.Latest == nil {
			continue
		}
		if metrics == nil {
			metrics = map[int64]*model.NodeMetrics{}
		}
		m := metrics[*c.NodeID]
		if m == nil {
			m = &model.NodeMetrics{NodeID: *c.NodeID}
			metrics[*c.NodeID] = m
		}
		applyAgentReport(m, &st.Latest.Report, st.Latest.At)
	}
	return metrics, nil
}

// applyAgentReport lays an agent's report over a machine's sample. Inside a
// container the SSH sample reads the container's own cgroup limits where it
// can, which the agent does not, so its CPU and memory are kept.
func applyAgentReport(m *model.NodeMetrics, r *probe.Report, at time.Time) {
	ptr := func(v int64) *int64 { return &v }
	fptr := func(v float64) *float64 { return &v }
	if m.Container == "" || m.MemSource != model.MemSourceCgroup {
		m.CPUPercent = fptr(r.CPU.Usage)
		m.MemTotal, m.MemUsed = ptr(r.RAM.Total), ptr(r.RAM.Used)
		m.SwapTotal, m.SwapUsed = ptr(r.Swap.Total), ptr(r.Swap.Used)
	}
	m.DiskTotal, m.DiskUsed = ptr(r.Disk.Total), ptr(r.Disk.Used)
	m.Load1, m.Load5, m.Load15 = fptr(r.Load.Load1), fptr(r.Load.Load5), fptr(r.Load.Load15)
	m.UptimeSec = ptr(r.Uptime)
	m.NetRxRate, m.NetTxRate = ptr(r.Network.Down), ptr(r.Network.Up)
	m.NetRxBytes, m.NetTxBytes = ptr(r.Network.TotalDown), ptr(r.Network.TotalUp)
	m.CollectedAt = at
	m.Source = "agent"
}

// matchProbes pairs probe agents with a machine just added or moved, and
// keeps the public page in the panel's order, in the background, since it may
// wait on looking the machine's name up.
func (s *Server) matchProbes(r *http.Request) {
	if s.probe == nil {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	go func() {
		s.probe.Link(ctx)
		if err := s.svc.Store().SyncProbeWeights(ctx); err != nil {
			s.log.Error("order probe machines", "error", err)
		}
	}()
}
