package verifier

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// runProbe executes the generated script the way a node's shell would, so the
// test covers the quoting and the embedded python rather than just the Go
// string that produces them.
func runProbe(t *testing.T, host string, port int) []string {
	t.Helper()
	for _, bin := range []string{"sh", "python3"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("这台机器上没有 %s，跳过", bin)
		}
	}
	out, err := exec.Command("sh", "-c", tcpProbeCommand(host, port)).Output()
	if err != nil {
		t.Fatalf("跑探测脚本失败: %v (输出 %q)", err, out)
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 {
		t.Fatalf("探测输出必须是两段 %q，实际拿到 %q", "ok|fail <毫秒>", out)
	}
	return fields
}

func TestTCPProbeReportsMillisecondsWhenTargetAccepts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	fields := runProbe(t, "127.0.0.1", ln.Addr().(*net.TCPAddr).Port)
	if fields[0] != "ok" {
		t.Fatalf("目标在监听，判定应为 ok，实际 %q", fields[0])
	}
	// The number matters as much as the verdict: an unparseable one is how a
	// hop silently loses its latency for good.
	if _, err := strconv.Atoi(fields[1]); err != nil {
		t.Fatalf("毫秒数必须能解析成整数，实际 %q", fields[1])
	}
}

func TestTCPProbeReportsFailWhenNothingListens(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	if fields := runProbe(t, "127.0.0.1", port); fields[0] != "fail" {
		t.Fatalf("端口没人监听，判定应为 fail，实际 %q", fields[0])
	}
}

// TestTCPProbeStartsClockAfterResolution guards the reason this probe was
// rewritten: with the timer started before name resolution, a DDNS landing
// answering in 26ms was reported as 250ms, because the lookup itself cost
// 243ms and was being counted as link latency.
//
// Every branch is checked, not just the first. An earlier version of this test
// only covered python, and the bash branch was subsequently written with the
// very flaw the test existed to prevent — it overstated a 27ms link as 30-38ms
// until a hand measurement caught it.
func TestTCPProbeStartsClockAfterResolution(t *testing.T) {
	script := tcpProbeCommand("example.invalid", 443)

	for _, tc := range []struct {
		branch  string
		resolve string
		clock   string
	}{
		{"python3", "getaddrinfo", "t=time.time()"},
		{"bash", "getent", "a=${EPOCHREALTIME"},
	} {
		resolve := strings.Index(script, tc.resolve)
		clock := strings.Index(script, tc.clock)
		if resolve < 0 || clock < 0 {
			t.Fatalf("%s 分支里应当同时有 %q 与计时起点 %q，实际:\n%s",
				tc.branch, tc.resolve, tc.clock, script)
		}
		if resolve > clock {
			t.Errorf("%s 分支的计时起点跑到了域名解析前面，DNS 又会被算进链路延迟",
				tc.branch)
		}
	}
}

// runProbeWithoutPython3 exercises the bash fallback by running the script with
// a PATH that cannot reach python3. That branch is the one minimal cloud images
// actually take, and it is the branch a Go-string assertion cannot check: the
// timing lives inside two levels of nested quoting.
func runProbeWithoutPython3(t *testing.T, host string, port int) []string {
	t.Helper()

	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("这台机器上没有 bash，跳过")
	}
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("这台机器上没有 sh，跳过")
	}

	env := []string{"PATH=" + filepath.Dir(bashPath)}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "PATH=") {
			env = append(env, kv)
		}
	}

	// If python3 is reachable anyway the script takes the first branch and this
	// test would silently assert nothing, so say so rather than pass falsely.
	probe := exec.Command(shPath, "-c", "command -v python3")
	probe.Env = env
	if out, _ := probe.Output(); len(strings.TrimSpace(string(out))) > 0 {
		t.Skipf("裁剪后的 PATH 里仍能找到 python3 (%s)，走不到 bash 分支，跳过",
			strings.TrimSpace(string(out)))
	}

	cmd := exec.Command(shPath, "-c", tcpProbeCommand(host, port))
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("跑探测脚本失败: %v (输出 %q)", err, out)
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 {
		t.Fatalf("探测输出必须是两段 %q，实际拿到 %q", "ok|fail <毫秒>", out)
	}
	return fields
}

func TestTCPProbeBashBranchTimesTheConnect(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	fields := runProbeWithoutPython3(t, "127.0.0.1", ln.Addr().(*net.TCPAddr).Port)
	if fields[0] != "ok" {
		t.Fatalf("目标在监听，判定应为 ok，实际 %q", fields[0])
	}
	// "-" is what this branch used to return unconditionally, and it is what
	// left hops permanently blank. A number is the whole point of the change.
	if _, err := strconv.Atoi(fields[1]); err != nil {
		t.Fatalf("bash 分支必须给出毫秒数而不是 %q", fields[1])
	}
}

// TestTCPProbeBashBranchNeverReportsOkOnRefusal guards the failure mode that
// would be worst here: bash's behaviour on a failed exec redirection differs
// between posix and default mode, and a version that fell through to the echo
// would report an unreachable landing as a healthy one.
func TestTCPProbeBashBranchNeverReportsOkOnRefusal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	if fields := runProbeWithoutPython3(t, "127.0.0.1", port); fields[0] != "fail" {
		t.Fatalf("端口没人监听，判定必须是 fail，实际 %q", fields)
	}
}
