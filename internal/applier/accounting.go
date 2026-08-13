package applier

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/sshx"
)

// acctChain holds fluxlite's byte counters.
//
// Every rule in it is target-less: it increments a counter and falls through.
// That is what makes putting fluxlite into INPUT and OUTPUT safe on a machine
// that already runs another forwarding tool — the rules cannot accept, drop or
// rewrite anything, so no ordering against someone else's rules can matter.
const acctChain = "FLUXLITE_ACCT"

// AcctState is what ensureAccounting found or did on a node.
type AcctState string

const (
	// AcctUnchanged means the counters were already correct and kept running.
	AcctUnchanged AcctState = "unchanged"
	// AcctRebuilt means the rules were replaced, so their counters restarted
	// from zero and the stored baseline is stale.
	AcctRebuilt AcctState = "rebuilt"
	// AcctUnavailable means the node has no usable iptables. Traffic for it is
	// unknown, which is not the same as zero.
	AcctUnavailable AcctState = "unavailable"
)

// ensureAccounting makes the node count the bytes crossing a hop's listen port.
//
// Existing rules are never rebuilt for their own sake: recreating a rule resets
// its kernel counter, so doing that on every reconcile would hold every total
// at roughly zero. Rules are only replaced when they no longer describe the
// port the hop actually listens on, and the caller is told when that happened
// so it can drop the now meaningless baseline.
func (a *Applier) ensureAccounting(ctx context.Context, client *ssh.Client, slug string, listen int) (AcctState, error) {
	res, err := sshx.Run(ctx, client, ensureAcctCommand(slug, listen))
	if err != nil {
		return AcctUnavailable, fmt.Errorf("install byte counters: %w", err)
	}
	switch strings.TrimSpace(res.Stdout) {
	case "ok-unchanged":
		return AcctUnchanged, nil
	case "ok-rebuilt":
		return AcctRebuilt, nil
	case "no-iptables":
		return AcctUnavailable, nil
	default:
		return AcctUnavailable, fmt.Errorf("install byte counters: %s",
			strings.TrimSpace(res.Stdout+" "+res.Stderr))
	}
}

func ensureAcctCommand(slug string, listen int) string {
	return fmt.Sprintf(`
CHAIN=%s
TAG="fluxlite:%s:"
L=%d

command -v iptables >/dev/null 2>&1 || { echo no-iptables; exit 0; }

iptables -nL "$CHAIN" >/dev/null 2>&1 || iptables -N "$CHAIN" 2>/dev/null || { echo no-iptables; exit 0; }
iptables -C INPUT -j "$CHAIN" 2>/dev/null || iptables -I INPUT 1 -j "$CHAIN" 2>/dev/null || { echo no-iptables; exit 0; }
iptables -C OUTPUT -j "$CHAIN" 2>/dev/null || iptables -I OUTPUT 1 -j "$CHAIN" 2>/dev/null || { echo no-iptables; exit 0; }

intact=1
for p in tcp udp; do
    iptables -C "$CHAIN" -p $p --dport "$L" -m comment --comment "${TAG}in"  2>/dev/null || intact=0
    iptables -C "$CHAIN" -p $p --sport "$L" -m comment --comment "${TAG}out" 2>/dev/null || intact=0
done
have=$(iptables -S "$CHAIN" 2>/dev/null | grep -c "$TAG" || true)

if [ "$intact" = 1 ] && [ "$have" = 4 ]; then
    echo ok-unchanged
    exit 0
fi

# 端口变过或规则缺失才走到这里。先清掉这条链路的全部旧规则——按旧端口计数
# 会把现在占用那个端口的别人的流量记到这条链路头上。
iptables -S "$CHAIN" 2>/dev/null | grep "$TAG" | sed 's/^-A /-D /' | while read -r rule; do
    eval "iptables $rule" 2>/dev/null || true
done

for p in tcp udp; do
    iptables -A "$CHAIN" -p $p --dport "$L" -m comment --comment "${TAG}in"  2>/dev/null || { echo failed; exit 0; }
    iptables -A "$CHAIN" -p $p --sport "$L" -m comment --comment "${TAG}out" 2>/dev/null || { echo failed; exit 0; }
done
echo ok-rebuilt`, acctChain, slug, listen)
}

// removeAccounting drops one route's counters from a node.
func removeAccounting(ctx context.Context, client *ssh.Client, slug string) error {
	cmd := fmt.Sprintf(`
command -v iptables >/dev/null 2>&1 || exit 0
iptables -S %s 2>/dev/null | grep "fluxlite:%s:" | sed 's/^-A /-D /' | while read -r rule; do
    eval "iptables $rule" 2>/dev/null || true
done
exit 0`, acctChain, slug)
	if _, err := sshx.Run(ctx, client, cmd); err != nil {
		return fmt.Errorf("remove byte counters: %w", err)
	}
	return nil
}

// HopCounters is one hop's raw kernel byte counters.
type HopCounters struct {
	In  uint64
	Out uint64
}

// acctLine picks the byte count and the owning route out of a counter row.
//
// Position is unreliable: a target-less rule leaves the target column empty, so
// the fields after it shift left by one. Only the first two columns (packets,
// bytes) and the comment are anchored, and those are all that is needed.
var acctLine = regexp.MustCompile(`^\s*\d+\s+(\d+)\s+.*/\* fluxlite:([a-z0-9][a-z0-9-]*):(in|out) \*/`)

// ReadCounters returns every route's raw counters on a node, keyed by slug.
//
// A node without the chain returns an empty map and no error: it has simply
// never had a route deployed since counting was added. Callers must not read
// an absent slug as zero traffic.
func (a *Applier) ReadCounters(ctx context.Context, node *model.Node) (map[string]HopCounters, error) {
	client, err := a.pool.Get(ctx, node)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", node.Name, err)
	}

	res, err := sshx.Run(ctx, client.Client,
		fmt.Sprintf(`iptables -nvxL %s 2>/dev/null || true`, acctChain))
	if err != nil {
		return nil, fmt.Errorf("read byte counters on %s: %w", node.Name, err)
	}

	out := make(map[string]HopCounters)
	for _, line := range strings.Split(res.Stdout, "\n") {
		m := acctLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		bytes, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			continue
		}
		c := out[m[2]]
		// tcp and udp are separate rules sharing one comment, so both fold
		// into the same direction.
		if m[3] == "in" {
			c.In += bytes
		} else {
			c.Out += bytes
		}
		out[m[2]] = c
	}
	return out, nil
}
