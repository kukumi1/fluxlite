package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/sshx"
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
	cache map[int64]map[int]bool
	// unreachable records nodes that could not be probed, so the failure is
	// reported once rather than retried for every hop.
	unreachable map[int64]error
}

func (s *Service) newLiveLookup(excludeRouteID *int64) *liveLookup {
	return &liveLookup{
		svc:            s,
		excludeRouteID: excludeRouteID,
		cache:          make(map[int64]map[int]bool),
		unreachable:    make(map[int64]error),
	}
}

func (l *liveLookup) NodeByID(ctx context.Context, id int64) (*model.Node, error) {
	return l.svc.store.NodeByID(ctx, id)
}

// UsedPortsOnNode merges fluxlite's own allocations with the ports currently
// bound on the machine.
func (l *liveLookup) UsedPortsOnNode(ctx context.Context, nodeID int64, excludeRouteID *int64) (map[int]bool, error) {
	used, err := l.svc.store.UsedPortsOnNode(ctx, nodeID, excludeRouteID)
	if err != nil {
		return nil, err
	}

	live, err := l.listeningPorts(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	for p := range live {
		used[p] = true
	}
	return used, nil
}

func (l *liveLookup) listeningPorts(ctx context.Context, nodeID int64) (map[int]bool, error) {
	if cached, ok := l.cache[nodeID]; ok {
		return cached, nil
	}
	if err, ok := l.unreachable[nodeID]; ok {
		return nil, err
	}

	node, err := l.svc.store.NodeByID(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	ports, err := l.svc.listeningPortsOn(ctx, node)
	if err != nil {
		wrapped := fmt.Errorf("cannot read ports in use on %s: %w", node.Name, err)
		l.unreachable[nodeID] = wrapped
		return nil, wrapped
	}
	l.cache[nodeID] = ports
	return ports, nil
}

// listeningPortsOn returns every local port with a listener on the node.
// ss is preferred; netstat is the fallback for images that lack iproute2.
func (s *Service) listeningPortsOn(ctx context.Context, node *model.Node) (map[int]bool, error) {
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return nil, err
	}

	const script = `
if command -v ss >/dev/null 2>&1; then
  ss -tuln 2>/dev/null | awk 'NR>1 {print $5}'
elif command -v netstat >/dev/null 2>&1; then
  netstat -tuln 2>/dev/null | awk '/^(tcp|udp)/ {print $4}'
else
  echo NOTOOL
fi`

	res, err := sshx.Run(ctx, client.Client, script)
	if err != nil {
		return nil, err
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "NOTOOL" {
		return nil, fmt.Errorf("neither ss nor netstat is available")
	}
	return parseListeningPorts(out), nil
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
