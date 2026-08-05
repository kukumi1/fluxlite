// Package verifier proves that a route actually carries traffic.
//
// It deliberately does not trust a successful TCP connect. realm accepts the
// client connection locally and only then dials the next hop, so a connect
// succeeds — and returns in single-digit milliseconds — even when the rest of
// the chain is dead. The only conclusive evidence is a packet capture on the
// final hop showing a marker payload leaving for the landing target and the
// target acknowledging it.
package verifier

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/planner"
	"github.com/kukumi1/fluxlite/internal/sshx"
)

// Verdict is the confidence level of a check.
type Verdict string

const (
	VerdictPass Verdict = "pass"
	VerdictFail Verdict = "fail"
	// VerdictUnknown means the check could not run. It is never collapsed into
	// a pass or a fail, because a missing tool must not look like a healthy
	// link nor like a broken one.
	VerdictUnknown Verdict = "unknown"
)

// Check is one step of a verification run.
type Check struct {
	Name    string  `json:"name"`
	Verdict Verdict `json:"verdict"`
	Detail  string  `json:"detail"`

	// LatencyMS is nil when the check carries no timing, either because it is
	// not a reachability probe or because the node had no timer with useful
	// resolution. Nil is never rendered as zero.
	LatencyMS *int `json:"latency_ms,omitempty"`

	// HopOrder ties a reachability check back to the hop that performed it, so
	// the measurement can be stored against that hop. It is nil for checks
	// that belong to the route as a whole.
	HopOrder *int `json:"-"`
}

// Report is the outcome of verifying a route.
type Report struct {
	RouteName string  `json:"route_name"`
	Checks    []Check `json:"checks"`
	// Proven is true only when the packet capture confirmed payload delivery.
	Proven bool `json:"proven"`
}

// Verifier runs verification against deployed routes.
type Verifier struct {
	pool *sshx.Pool
}

func New(pool *sshx.Pool) *Verifier {
	return &Verifier{pool: pool}
}

// Verify checks a deployed route hop by hop and then proves end-to-end
// delivery with a capture on the final hop.
func (v *Verifier) Verify(ctx context.Context, plan *planner.Plan) (*Report, error) {
	report := &Report{RouteName: plan.Route.Name}

	for i := 0; i < len(plan.Hops)-1; i++ {
		from := plan.Hops[i]
		to := plan.Hops[i+1]
		check := v.checkReachable(ctx, from.Node, to.Node.Host, to.Listen,
			fmt.Sprintf("hop %d (%s) -> hop %d (%s)", i, from.Node.Name, i+1, to.Node.Name))
		check.HopOrder = &plan.Hops[i].HopOrder
		report.Checks = append(report.Checks, check)
	}

	last := plan.Hops[len(plan.Hops)-1]
	host, portStr, err := splitTarget(plan.Route.Target)
	if err != nil {
		return nil, err
	}
	lastCheck := v.checkReachable(ctx, last.Node, host, portStr,
		fmt.Sprintf("hop %d (%s) -> target %s", last.HopOrder, last.Node.Name, plan.Route.Target))
	lastCheck.HopOrder = &plan.Hops[len(plan.Hops)-1].HopOrder
	report.Checks = append(report.Checks, lastCheck)

	proof, proven := v.proveDelivery(ctx, plan, host, portStr)
	report.Checks = append(report.Checks, proof)
	report.Proven = proven

	return report, nil
}

// MeasureLatencies times each hop's reach to whatever it forwards to, keyed by
// hop order.
//
// This is the reachability half of Verify without the marker injection and
// packet capture, which are far too expensive to run on a schedule. It proves
// nothing about delivery — only Verify does that — but it is cheap enough to
// keep the route list's numbers current.
//
// Hops that could not be timed are absent from the result rather than present
// as zero, so a node without a usable timer never overwrites a good reading.
func (v *Verifier) MeasureLatencies(ctx context.Context, plan *planner.Plan) (map[int]int, error) {
	host, port, err := splitTarget(plan.Route.Target)
	if err != nil {
		return nil, err
	}

	out := make(map[int]int, len(plan.Hops))
	for i, hop := range plan.Hops {
		dialHost, dialPort := host, port
		if i < len(plan.Hops)-1 {
			dialHost, dialPort = plan.Hops[i+1].Node.Host, plan.Hops[i+1].Listen
		}
		check := v.checkReachable(ctx, hop.Node, dialHost, dialPort, "latency")
		if check.LatencyMS != nil {
			out[hop.HopOrder] = *check.LatencyMS
		}
	}
	return out, nil
}

// checkReachable opens a TCP connection from a node to an address and times it.
func (v *Verifier) checkReachable(ctx context.Context, from *model.Node, host string, port int, name string) Check {
	client, err := v.pool.Get(ctx, from)
	if err != nil {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "cannot reach node: " + err.Error()}
	}

	res, err := sshx.Run(ctx, client.Client, tcpProbeCommand(host, port))
	if err != nil {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "probe failed: " + err.Error()}
	}

	fields := strings.Fields(strings.TrimSpace(res.Stdout))
	if len(fields) < 2 {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "probe produced no result"}
	}
	var latency *int
	if ms, err := strconv.Atoi(fields[1]); err == nil {
		latency = &ms
	}
	switch fields[0] {
	case "none":
		return Check{Name: name, Verdict: VerdictUnknown,
			Detail: "node has no usable probe tool (need python3, bash or nc)"}
	case "fail":
		// A failed connect's timing measures how long the failure took, which
		// says nothing about the link, so it is discarded.
		return Check{Name: name, Verdict: VerdictFail, Detail: "connection refused or timed out"}
	}
	return Check{
		Name:      name,
		Verdict:   VerdictPass,
		Detail:    "TCP connect succeeded (does not by itself prove the payload is relayed)",
		LatencyMS: latency,
	}
}

// tcpProbeCommand builds a portable connectivity check.
//
// bash's /dev/tcp is unavailable on dash and on Alpine's busybox shell, so the
// probe falls back through several implementations. Output is always
// "ok|fail <milliseconds>", with "-" when no timer of useful resolution is
// available (busybox date has no %N).
func tcpProbeCommand(host string, port int) string {
	return fmt.Sprintf(`
if command -v python3 >/dev/null 2>&1; then
  python3 -c 'import socket,time,sys
t=time.time()
try:
    socket.create_connection(("%s",%d),8).close()
    print("ok %%d"%%((time.time()-t)*1000))
except Exception:
    print("fail %%d"%%((time.time()-t)*1000))'
elif command -v bash >/dev/null 2>&1; then
  bash -c 'exec 3<>/dev/tcp/%s/%d' 2>/dev/null && echo "ok -" || echo "fail -"
elif command -v nc >/dev/null 2>&1; then
  nc -z -w 8 %s %d >/dev/null 2>&1 && echo "ok -" || echo "fail -"
else
  echo "none -"
fi`, host, port, host, port, host, port)
}

// tcpInjectCommand opens a connection and writes a marker, holding it open
// briefly so the relay chain has time to forward the payload.
func tcpInjectCommand(host string, port int, marker string) string {
	return fmt.Sprintf(`
if command -v python3 >/dev/null 2>&1; then
  python3 -c 'import socket,time
try:
    s=socket.create_connection(("%s",%d),8)
    s.sendall(b"%s\n")
    time.sleep(3)
    s.close()
    print("injected")
except Exception as e:
    print("inject-failed: %%s"%%e)'
elif command -v bash >/dev/null 2>&1; then
  bash -c '(exec 3<>/dev/tcp/%s/%d; printf "%s\n" >&3; sleep 3; exec 3<&-) 2>/dev/null && echo injected || echo inject-failed'
else
  echo "inject-unavailable"
fi`, host, port, marker, host, port, marker)
}

var tcpdumpFlagsRe = regexp.MustCompile(`Flags \[([^\]]+)\]`)

// proveDelivery captures traffic on the final hop while injecting a marker at
// the route entry, then confirms the marker left for the target.
func (v *Verifier) proveDelivery(ctx context.Context, plan *planner.Plan, targetHost string, targetPort int) (Check, bool) {
	const name = "end-to-end payload delivery (packet capture)"

	last := plan.Hops[len(plan.Hops)-1]
	entry := plan.Hops[0]

	lastClient, err := v.pool.Get(ctx, last.Node)
	if err != nil {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "cannot reach final hop: " + err.Error()}, false
	}
	entryClient, err := v.pool.Get(ctx, entry.Node)
	if err != nil {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "cannot reach entry hop: " + err.Error()}, false
	}

	if res, err := sshx.Run(ctx, lastClient.Client, "command -v tcpdump"); err != nil || res.ExitCode != 0 {
		return Check{
			Name:    name,
			Verdict: VerdictUnknown,
			Detail:  "tcpdump is not installed on the final hop, so delivery could not be proven",
		}, false
	}

	marker, err := cryptox.RandomToken(9)
	if err != nil {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "could not generate marker: " + err.Error()}, false
	}
	// Keep the marker alphanumeric so it survives tcpdump's ASCII rendering
	// and grep without escaping surprises.
	marker = "FLUXLITE" + strings.NewReplacer("-", "", "_", "").Replace(marker)

	capturePath := "/tmp/.fluxlite-verify-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	// Two things here are load-bearing:
	//
	// -U makes tcpdump flush every packet to the file immediately. Without it
	// output is block buffered, so a low-traffic port leaves the capture file
	// empty while a busy port happens to fill the buffer — the check would
	// then pass or fail based on unrelated background traffic.
	//
	// The filter matches on port alone. Filtering by hostname makes tcpdump
	// resolve the name itself, and a DDNS target can resolve to a different
	// address than the one realm actually dialled.
	startCapture := fmt.Sprintf(
		`(setsid timeout 25 tcpdump -i any -nn -A -U 'tcp port %d' > %s 2>/dev/null &) ; sleep 3`,
		targetPort, capturePath)

	defer func() {
		cleanup := fmt.Sprintf("pkill -f 'tcpdump -i any -nn -A -U' 2>/dev/null; rm -f %s", capturePath)
		_, _ = sshx.Run(context.WithoutCancel(ctx), lastClient.Client, cleanup)
	}()

	if _, err := sshx.Run(ctx, lastClient.Client, startCapture); err != nil {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "could not start capture: " + err.Error()}, false
	}

	// Inject at the entry hop's own listener so the marker traverses the whole
	// chain exactly as client traffic would.
	injectRes, err := sshx.Run(ctx, entryClient.Client, tcpInjectCommand("127.0.0.1", entry.Listen, marker))
	if err != nil {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "could not inject marker: " + err.Error()}, false
	}
	if injected := strings.TrimSpace(injectRes.Stdout); !strings.HasPrefix(injected, "injected") {
		// Failing to even connect to the entry listener is a different fault
		// from the chain dropping the payload, and must not be reported as one.
		return Check{
			Name:    name,
			Verdict: VerdictFail,
			Detail:  fmt.Sprintf("could not connect to the entry listener on %s: %s", entry.Node.Name, injected),
		}, false
	}

	time.Sleep(4 * time.Second)

	// Stop the capture before reading it. Even with -U this guarantees the
	// file is complete rather than racing a writer.
	if _, err := sshx.Run(ctx, lastClient.Client, "pkill -f 'tcpdump -i any -nn -A -U' 2>/dev/null; sleep 1"); err != nil {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "could not stop capture: " + err.Error()}, false
	}

	res, err := sshx.Run(ctx, lastClient.Client,
		fmt.Sprintf(`grep -c %s %s 2>/dev/null || echo 0; grep -c 'IP ' %s 2>/dev/null || echo 0; grep -o 'Flags \[[^]]*\]' %s 2>/dev/null | head -8`,
			marker, capturePath, capturePath, capturePath))
	if err != nil {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "could not read capture: " + err.Error()}, false
	}

	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	hits, packets := 0, 0
	if len(lines) > 0 {
		hits, _ = strconv.Atoi(strings.TrimSpace(lines[0]))
	}
	if len(lines) > 1 {
		packets, _ = strconv.Atoi(strings.TrimSpace(lines[1]))
	}
	flags := tcpdumpFlagsRe.FindAllString(res.Stdout, -1)

	if hits > 0 {
		return Check{
			Name:    name,
			Verdict: VerdictPass,
			Detail: fmt.Sprintf("marker %s observed leaving %s for %s:%d (%s)",
				marker, last.Node.Name, targetHost, targetPort, strings.Join(flags, " ")),
		}, true
	}

	// Distinguishing "captured nothing at all" from "captured traffic but not
	// ours" tells the operator whether to suspect the chain or the capture.
	if packets == 0 {
		return Check{
			Name:    name,
			Verdict: VerdictUnknown,
			Detail: fmt.Sprintf("capture on %s recorded no packets on port %d at all; cannot conclude anything about delivery",
				last.Node.Name, targetPort),
		}, false
	}
	return Check{
		Name:    name,
		Verdict: VerdictFail,
		Detail: fmt.Sprintf("capture on %s saw %d packets on port %d but none carried the marker injected at %s; the chain accepts connections without relaying payload",
			last.Node.Name, packets, targetPort, entry.Node.Name),
	}, false
}

// LatencyComparison measures the landing target from the final hop and from
// the entry hop, so an operator can tell whether relaying through the chain
// actually beats connecting directly.
func (v *Verifier) LatencyComparison(ctx context.Context, plan *planner.Plan) ([]Check, error) {
	host, _, err := splitTarget(plan.Route.Target)
	if err != nil {
		return nil, err
	}

	var checks []Check
	for _, hop := range []planner.HopPlan{plan.Hops[0], plan.Hops[len(plan.Hops)-1]} {
		client, err := v.pool.Get(ctx, hop.Node)
		if err != nil {
			checks = append(checks, Check{
				Name:    fmt.Sprintf("%s -> %s ping", hop.Node.Name, host),
				Verdict: VerdictUnknown,
				Detail:  err.Error(),
			})
			continue
		}
		res, err := sshx.Run(ctx, client.Client,
			fmt.Sprintf(`ping -c 5 -W 2 %s 2>&1 | tail -2`, host))
		if err != nil {
			checks = append(checks, Check{
				Name:    fmt.Sprintf("%s -> %s ping", hop.Node.Name, host),
				Verdict: VerdictUnknown,
				Detail:  err.Error(),
			})
			continue
		}
		checks = append(checks, Check{
			Name:    fmt.Sprintf("%s -> %s ping", hop.Node.Name, host),
			Verdict: VerdictPass,
			Detail:  strings.TrimSpace(res.Stdout),
		})
	}
	return checks, nil
}

func splitTarget(target string) (string, int, error) {
	if err := model.ValidateTarget(target); err != nil {
		return "", 0, err
	}
	i := strings.LastIndex(target, ":")
	host := strings.Trim(target[:i], "[]")
	port, err := strconv.Atoi(target[i+1:])
	if err != nil {
		return "", 0, fmt.Errorf("parse target port: %w", err)
	}
	return host, port, nil
}
