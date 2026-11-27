package api

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kukumi1/fluxlite/internal/komari"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/probe"
	"github.com/kukumi1/fluxlite/internal/service"
	"github.com/kukumi1/fluxlite/internal/store"
)

// A machine enrolled while the probe is on gets an agent identity paired
// with it, which it keeps when enrolled again; with no public domain there
// is nowhere to report, and the installer is told nothing.
func TestEnrollGivesTheMachineAnAgent(t *testing.T) {
	s, st := newAuditTestServer(t)
	ctx := context.Background()
	s.probe = probe.New(st, slog.Default())
	n := &model.Node{Name: "hk", Host: "192.0.2.10", SSHPort: 22, SSHUser: "root", AuthType: model.AuthKey, AuthSecret: []byte("secret"), PortStart: 10000, PortEnd: 20000}
	if err := st.CreateNode(ctx, n); err != nil {
		t.Fatal(err)
	}
	outcome := &service.EnrollOutcome{NodeID: n.ID, Name: n.Name}

	if endpoint, token := s.enrollAgent(ctx, outcome); endpoint != "" || token != "" {
		t.Fatal("without the public domain no agent is set up")
	}
	if err := st.SetProbeSetting(ctx, komari.PublicHostSetting, "status.example.com"); err != nil {
		t.Fatal(err)
	}
	endpoint, token := s.enrollAgent(ctx, outcome)
	if endpoint != "https://status.example.com" || token == "" {
		t.Fatal(endpoint, token)
	}
	c, err := st.ProbeClientByToken(ctx, token)
	if err != nil || c.NodeID == nil || *c.NodeID != n.ID || c.Name != "hk" {
		t.Fatalf("the agent is the machine's from the start: %+v %v", c, err)
	}
	if _, again := s.enrollAgent(ctx, outcome); again != token {
		t.Fatal("a machine enrolled again keeps its agent and history")
	}
	if list, _ := st.ProbeClients(ctx); len(list) != 1 {
		t.Fatalf("one machine, one agent: %d", len(list))
	}
}

// Deleting a probe machine takes it out of every latency check, and
// machines already gone are dropped when the probe starts.
func TestLatencyChecksForgetDeletedMachines(t *testing.T) {
	_, st := newAuditTestServer(t)
	ctx := context.Background()
	err := st.ReplaceProbe(ctx, store.ProbeImport{
		Clients: []store.ProbeClient{{UUID: "a", Token: "ta", Name: "A"}, {UUID: "b", Token: "tb", Name: "B"}},
		Tasks:   []store.ProbeTask{{ID: 7, Name: "成都联通", Type: "tcp", Target: "192.0.2.1:443", Interval: 60, Clients: []string{"a", "b", "gone"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PruneProbeTaskClients(ctx); err != nil {
		t.Fatal(err)
	}
	if tasks, _ := st.ProbeTasks(ctx); len(tasks[0].Clients) != 2 {
		t.Fatalf("a machine already gone is dropped: %v", tasks[0].Clients)
	}
	if err := st.DeleteProbeClient(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if tasks, _ := st.ProbeTasks(ctx); len(tasks[0].Clients) != 1 || tasks[0].Clients[0] != "b" {
		t.Fatalf("a deleted machine leaves its checks: %v", tasks[0].Clients)
	}
}

// The installer is plain POSIX sh: Alpine has no bash.
func TestEnrollScriptParses(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to check the script with")
	}
	path := filepath.Join(t.TempDir(), "enroll.sh")
	if err := os.WriteFile(path, []byte(enrollScript), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sh, "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
