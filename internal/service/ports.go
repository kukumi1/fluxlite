package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/sshx"
	"github.com/kukumi1/fluxlite/internal/store"
)

// liveLookup answers port-allocation queries with both the ports fluxlite has
// already handed out and the ports something else on the machine is listening
// on.
//
// Without the second half, a wide port pool would eventually allocate a port
// that sshd, a web server or another proxy already holds. realm would fail to
// bind and sit in a restart loop, and the operator would have no idea why.
type liveLookup struct {
	svc            *Service
	excludeRouteID *int64

	// cache holds one probe result per node for the lifetime of a single
	// allocation, so a five-hop route does not open five sessions per node.
	cache map[int64]portClaims
	// unreachable records nodes that could not be probed, so the failure is
	// reported once rather than retried for every hop.
	unreachable map[int64]error

	// own maps a node to the ports the route being edited already holds there.
	own       map[int64]map[int]bool
	ownLoaded bool
}

func (s *Service) newLiveLookup(excludeRouteID *int64) *liveLookup {
	return &liveLookup{
		svc:            s,
		excludeRouteID: excludeRouteID,
		cache:          make(map[int64]portClaims),
		unreachable:    make(map[int64]error),
	}
}

func (l *liveLookup) NodeByID(ctx context.Context, id int64) (*model.Node, error) {
	return l.svc.store.NodeByID(ctx, id)
}

// UsedPortsOnNode merges fluxlite's own allocations with the ports the machine
// has already spoken for, whether by a listening socket or by a NAT rule.
func (l *liveLookup) UsedPortsOnNode(ctx context.Context, nodeID int64, excludeRouteID *int64) (map[int]bool, error) {
	used, err := l.svc.store.UsedPortsOnNode(ctx, nodeID, excludeRouteID)
	if err != nil {
		return nil, err
	}

	claimed, err := l.claimedPorts(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	// A deployed route holds its own listeners open, and the socket scan cannot
	// tell whose they are. Counting them would make every deployed route
	// impossible to edit: its entry port would be reported as taken by itself.
	own, err := l.ownPortsOn(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	for p := range claimed.sockets {
		if !own[p] {
			used[p] = true
		}
	}

	// NAT claims are never fluxlite's own — it installs relays, not rules — so
	// they are absolute.
	for p := range claimed.nat {
		used[p] = true
	}
	return used, nil
}

// ownPortsOn returns the ports the excluded route currently occupies on a node.
func (l *liveLookup) ownPortsOn(ctx context.Context, nodeID int64) (map[int]bool, error) {
	if l.excludeRouteID == nil {
		return nil, nil
	}
	if !l.ownLoaded {
		route, err := l.svc.store.RouteByID(ctx, *l.excludeRouteID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		l.own = make(map[int64]map[int]bool)
		if route != nil {
			for _, h := range route.Hops {
				if l.own[h.NodeID] == nil {
					l.own[h.NodeID] = make(map[int]bool)
				}
				l.own[h.NodeID][h.RelayPort] = true
			}
		}
		l.ownLoaded = true
	}
	return l.own[nodeID], nil
}

func (l *liveLookup) claimedPorts(ctx context.Context, nodeID int64) (portClaims, error) {
	if cached, ok := l.cache[nodeID]; ok {
		return cached, nil
	}
	if err, ok := l.unreachable[nodeID]; ok {
		return portClaims{}, err
	}

	node, err := l.svc.store.NodeByID(ctx, nodeID)
	if err != nil {
		return portClaims{}, err
	}

	claims, err := l.svc.claimedPortsOn(ctx, node)
	if err != nil {
		wrapped := fmt.Errorf("cannot read ports in use on %s: %w", node.Name, err)
		l.unreachable[nodeID] = wrapped
		return portClaims{}, wrapped
	}
	l.cache[nodeID] = claims
	return claims, nil
}

// portClaims separates the two ways a port can already be spoken for, because
// the two are corrected differently and only one of them can belong to us.
type portClaims struct {
	// sockets are ports something is bound to and listening on.
	sockets map[int]bool
	// nat are ports a DNAT or REDIRECT rule diverts before anything local
	// sees them.
	nat map[int]bool
}

const natMarker = "#fluxlite-nat#"

// claimedPortsOn reports which ports on a node are already taken.
//
// A listening socket is the obvious case. The dangerous case is a NAT rule:
// DNAT in PREROUTING rewrites a packet's destination before it can reach any
// local socket, so a relay bound to that port starts cleanly, appears in ss,
// and never receives a byte of the traffic aimed at it. Nothing about it looks
// broken. Port managers that publish rules rather than open sockets — sing-box
// managers, Docker, hand-written firewall scripts — all produce this, and a
// socket scan is blind to every one of them.
//
// nftables-only hosts are not parsed: sing-box's iptables backend requires
// iptables-save and falls back to socat where it is missing, and socat does
// open a socket, so those claims are caught by the scan above.
func (s *Service) claimedPortsOn(ctx context.Context, node *model.Node) (portClaims, error) {
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return portClaims{}, err
	}

	script := `
if command -v ss >/dev/null 2>&1; then
  ss -tuln 2>/dev/null | awk 'NR>1 {print $5}'
elif command -v netstat >/dev/null 2>&1; then
  netstat -tuln 2>/dev/null | awk '/^(tcp|udp)/ {print $4}'
else
  echo NOTOOL
fi
echo '` + natMarker + `'
if command -v iptables-save >/dev/null 2>&1; then
  iptables-save -t nat 2>/dev/null | grep -E '\-j (DNAT|REDIRECT)' || true
fi`

	res, err := sshx.Run(ctx, client.Client, script)
	if err != nil {
		return portClaims{}, err
	}

	socketOut, natOut, _ := strings.Cut(res.Stdout, natMarker)
	socketOut = strings.TrimSpace(socketOut)
	if socketOut == "NOTOOL" {
		return portClaims{}, fmt.Errorf("neither ss nor netstat is available")
	}
	return portClaims{
		sockets: parseListeningPorts(socketOut),
		nat:     parseNATPorts(natOut),
	}, nil
}

var (
	dportRe  = regexp.MustCompile(`--dport\s+(\d+)(?::(\d+))?`)
	dportsRe = regexp.MustCompile(`--dports\s+([\d,:]+)`)
)

// maxNATRange bounds how far a port range is expanded. A rule covering a huge
// span is a policy about the whole machine rather than a claim on particular
// ports, and materialising it would say nothing useful while costing memory.
const maxNATRange = 4096

// parseNATPorts collects the destination ports that iptables nat rules divert.
func parseNATPorts(out string) map[int]bool {
	ports := make(map[int]bool)
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "-j DNAT") && !strings.Contains(line, "-j REDIRECT") {
			continue
		}
		if m := dportRe.FindStringSubmatch(line); m != nil {
			addPortRange(ports, m[1], m[2])
		}
		if m := dportsRe.FindStringSubmatch(line); m != nil {
			for _, part := range strings.Split(m[1], ",") {
				lo, hi, _ := strings.Cut(part, ":")
				addPortRange(ports, lo, hi)
			}
		}
	}
	return ports
}

func addPortRange(ports map[int]bool, lo, hi string) {
	start, err := strconv.Atoi(lo)
	if err != nil || start < 1 || start > 65535 {
		return
	}
	end := start
	if hi != "" {
		if parsed, err := strconv.Atoi(hi); err == nil && parsed >= start && parsed <= 65535 {
			end = parsed
		}
	}
	if end-start > maxNATRange {
		return
	}
	for p := start; p <= end; p++ {
		ports[p] = true
	}
}

// parseListeningPorts extracts port numbers from the local-address column of
// ss or netstat output. The column takes several shapes depending on address
// family and tool: 0.0.0.0:22, [::]:22, *:8080, 127.0.0.53%lo:53.
func parseListeningPorts(out string) map[int]bool {
	ports := make(map[int]bool)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		i := strings.LastIndex(line, ":")
		if i < 0 || i == len(line)-1 {
			continue
		}
		field := line[i+1:]
		// An interface suffix can trail the port on some kernels.
		if j := strings.IndexByte(field, '%'); j >= 0 {
			field = field[:j]
		}
		port, err := strconv.Atoi(field)
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		ports[port] = true
	}
	return ports
}
