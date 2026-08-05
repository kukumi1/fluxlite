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
	Latency string  `json:"latency,omitempty"`
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
		report.Checks = append(report.Checks, check)
	}

	last := plan.Hops[len(plan.Hops)-1]
	host, portStr, err := splitTarget(plan.Route.Target)
	if err != nil {
		return nil, err
	}
	report.Checks = append(report.Checks,
		v.checkReachable(ctx, last.Node, host, portStr,
			fmt.Sprintf("hop %d (%s) -> target %s", last.HopOrder, last.Node.Name, plan.Route.Target)))

	proof, proven := v.proveDelivery(ctx, plan, host, portStr)
	report.Checks = append(report.Checks, proof)
	report.Proven = proven

	return report, nil
}

// checkReachable opens a TCP connection from a node to an address and times it.
func (v *Verifier) checkReachable(ctx context.Context, from *model.Node, host string, port int, name string) Check {
	client, err := v.pool.Get(ctx, from)
	if err != nil {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "cannot reach node: " + err.Error()}
	}

	cmd := fmt.Sprintf(
		`s=$(date +%%s%%N); timeout 8 sh -c 'cat < /dev/null > /dev/tcp/%s/%d' 2>/dev/null && r=ok || r=fail; e=$(date +%%s%%N); echo "$r $(( (e-s)/1000000 ))"`,
		host, port)
	res, err := sshx.Run(ctx, client.Client, cmd)
	if err != nil {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "probe failed: " + err.Error()}
	}

	fields := strings.Fields(strings.TrimSpace(res.Stdout))
	if len(fields) < 2 {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "probe produced no result"}
	}
	latency := fields[1] + "ms"
	if fields[0] != "ok" {
		return Check{Name: name, Verdict: VerdictFail, Detail: "connection refused or timed out", Latency: latency}
	}
	return Check{
		Name:    name,
		Verdict: VerdictPass,
		Detail:  "TCP connect succeeded (does not by itself prove the payload is relayed)",
		Latency: latency,
	}
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
	startCapture := fmt.Sprintf(
		`(setsid timeout 25 tcpdump -i any -nn -A 'host %s and port %d' > %s 2>/dev/null &) ; sleep 3`,
		targetHost, targetPort, capturePath)

	defer func() {
		cleanup := fmt.Sprintf("pkill -f 'tcpdump -i any -nn -A' 2>/dev/null; rm -f %s", capturePath)
		_, _ = sshx.Run(context.WithoutCancel(ctx), lastClient.Client, cleanup)
	}()

	if _, err := sshx.Run(ctx, lastClient.Client, startCapture); err != nil {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "could not start capture: " + err.Error()}, false
	}

	// Inject at the entry hop's own listener so the marker traverses the whole
	// chain exactly as client traffic would.
	inject := fmt.Sprintf(
		`(exec 3<>/dev/tcp/127.0.0.1/%d; printf '%s\n' >&3; sleep 3; exec 3<&-) 2>/dev/null; echo injected`,
		entry.Listen, marker)
	if _, err := sshx.Run(ctx, entryClient.Client, inject); err != nil {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "could not inject marker: " + err.Error()}, false
	}

	time.Sleep(4 * time.Second)

	res, err := sshx.Run(ctx, lastClient.Client,
		fmt.Sprintf(`grep -c %s %s 2>/dev/null || echo 0; grep -o 'Flags \[[^]]*\]' %s 2>/dev/null | head -8`,
			marker, capturePath, capturePath))
	if err != nil {
		return Check{Name: name, Verdict: VerdictUnknown, Detail: "could not read capture: " + err.Error()}, false
	}

	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	hits := 0
	if len(lines) > 0 {
		hits, _ = strconv.Atoi(strings.TrimSpace(lines[0]))
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
	return Check{
		Name:    name,
		Verdict: VerdictFail,
		Detail: fmt.Sprintf("marker injected at %s never reached %s:%d; the chain accepts connections but does not relay payload",
			entry.Node.Name, targetHost, targetPort),
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
