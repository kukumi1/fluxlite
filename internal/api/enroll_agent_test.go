package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	// A machine that so far reported only through its agent keeps that agent
	// when it is enrolled for SSH.
	if err := st.CreateProbeClient(ctx, store.ProbeClient{UUID: "agent-only", Token: "t-agent", Name: "喵喵2", Info: []byte(`{"ipv4":"198.51.100.7"}`)}); err != nil {
		t.Fatal(err)
	}
	m := &model.Node{Name: "喵喵2", Host: "198.51.100.7", SSHPort: 22, SSHUser: "root", AuthType: model.AuthKey, AuthSecret: []byte("secret"), PortStart: 10000, PortEnd: 20000}
	if err := st.CreateNode(ctx, m); err != nil {
		t.Fatal(err)
	}
	if _, token := s.enrollAgent(ctx, &service.EnrollOutcome{NodeID: m.ID, Name: m.Name}); token != "t-agent" {
		t.Fatalf("the agent already there is kept, not a second one made: %q", token)
	}
	if c, _ := st.ProbeClientByToken(ctx, "t-agent"); c.NodeID == nil || *c.NodeID != m.ID {
		t.Fatal("and it is paired with the machine")
	}
	if list, _ := st.ProbeClients(ctx); len(list) != 2 {
		t.Fatalf("two machines, two agents: %d", len(list))
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

// The installer reads the agent's endpoint and token out of the panel's
// reply. Checking the script only parses once let a mangled sed replacement
// through, which installed agents with no address at all; so the lines are
// run, against a reply as the panel writes it.
func TestEnrollScriptReadsTheAgentIdentity(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to run the script with")
	}
	for i, r := range enrollScript {
		if r < 0x20 && r != '\n' && r != '\t' {
			t.Fatalf("control character %#x at byte %d", r, i)
		}
	}
	var lines []string
	for _, line := range strings.Split(enrollScript, "\n") {
		if strings.HasPrefix(line, "AGENT_ENDPOINT=") || strings.HasPrefix(line, "AGENT_TOKEN=") {
			lines = append(lines, line)
		}
	}
	if len(lines) != 2 {
		t.Fatalf("expected the two lines that read the agent identity, found %d", len(lines))
	}
	reply, err := json.Marshal(enrollReply{
		EnrollOutcome: &service.EnrollOutcome{NodeID: 7, Name: "喵喵2", Verified: true, UDPStatus: "通"},
		AgentEndpoint: "https://status.example.com", AgentToken: "AbC123xyz",
	})
	if err != nil {
		t.Fatal(err)
	}
	script := "RESP='" + string(reply) + "'\n" + strings.Join(lines, "\n") + "\nprintf '%s|%s' \"$AGENT_ENDPOINT\" \"$AGENT_TOKEN\"\n"
	out, err := exec.Command(sh, "-c", script).CombinedOutput()
	if err != nil || string(out) != "https://status.example.com|AbC123xyz" {
		t.Fatalf("%v: %q", err, out)
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
