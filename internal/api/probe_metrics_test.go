package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/probe"
	"github.com/kukumi1/fluxlite/internal/store"
)

// A machine with a reporting agent shows the agent's readings, live; the
// panel's SSH sample stays for what the agent does not report.
func TestNodeMetricsPreferTheAgent(t *testing.T) {
	s, st := newAuditTestServer(t)
	ctx := context.Background()
	hub := probe.New(st, slog.Default())
	s.probe = hub

	paired := &model.Node{Name: "hk", Host: "192.0.2.10", SSHPort: 22, SSHUser: "root", AuthType: model.AuthKey, AuthSecret: []byte("secret"), PortStart: 10000, PortEnd: 20000}
	sshOnly := &model.Node{Name: "jp", Host: "192.0.2.20", SSHPort: 22, SSHUser: "root", AuthType: model.AuthKey, AuthSecret: []byte("secret"), PortStart: 10000, PortEnd: 20000}
	for _, n := range []*model.Node{paired, sshOnly} {
		if err := st.CreateNode(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	cpu, kernel := 3.0, "6.1"
	for _, id := range []int64{paired.ID, sshOnly.ID} {
		if err := st.UpsertNodeMetrics(ctx, &model.NodeMetrics{NodeID: id, CPUPercent: &cpu, Kernel: kernel}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.ReplaceProbe(ctx, store.ProbeImport{Clients: []store.ProbeClient{{UUID: "u-1", Token: "token-1", Name: "HK"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProbeNode(ctx, "u-1", &paired.ID); err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Route("/api", hub.Routes)
	srv := httptest.NewServer(r)
	defer srv.Close()
	body := `{"jsonrpc":"2.0","method":"agent.report","params":{"report":{"cpu":{"usage":42},"ram":{"total":1000,"used":250},"swap":{"total":0,"used":0},
		"load":{"load1":0.5,"load5":0.4,"load15":0.3},"disk":{"total":5000,"used":2500},
		"network":{"up":100,"down":300,"totalUp":7000,"totalDown":9000},"connections":{"tcp":1,"udp":1},"uptime":3600,"process":9}}}`
	resp, err := http.Post(srv.URL+"/api/clients/v2/rpc?token=token-1", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	metrics, err := s.nodeMetrics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := metrics[paired.ID]
	if m == nil || m.Source != "agent" || *m.CPUPercent != 42 || *m.MemUsed != 250 || *m.NetRxRate != 300 || m.Kernel != kernel {
		t.Fatalf("the paired machine reads the agent's report over its sample: %+v", m)
	}
	if o := metrics[sshOnly.ID]; o == nil || o.Source != "" || *o.CPUPercent != cpu {
		t.Fatalf("a machine without an agent keeps its SSH sample: %+v", o)
	}
}

// Inside a container whose own limits the SSH sample could read, its CPU and
// memory stand; the agent reads the host's.
func TestAgentReportKeepsAContainersOwnLimits(t *testing.T) {
	cpu, mem := 7.0, int64(100)
	m := &model.NodeMetrics{Container: "lxc", MemSource: model.MemSourceCgroup, CPUPercent: &cpu, MemUsed: &mem}
	var r probe.Report
	r.CPU.Usage, r.RAM.Used, r.Load.Load1 = 90, 9000, 2
	applyAgentReport(m, &r, m.CollectedAt)
	if *m.CPUPercent != 7 || *m.MemUsed != 100 || *m.Load1 != 2 {
		t.Fatalf("%+v", m)
	}
}
