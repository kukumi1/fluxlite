// Package planner turns a route definition into the concrete realm
// configuration each hop must run.
//
// A route is a chain: hop 0 accepts client traffic and relays it to hop 1,
// hop 1 to hop 2, and the final hop dials the landing target. Each hop runs
// its own realm instance so that changing one route never disturbs another
// route on the same machine — realm has no config reload, so a shared
// process would mean a shared outage.
package planner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/kukumi1/fluxlite/internal/model"
)

// NodeLookup resolves nodes and their current port usage.
type NodeLookup interface {
	NodeByID(ctx context.Context, id int64) (*model.Node, error)
	UsedPortsOnNode(ctx context.Context, nodeID int64, excludeRouteID *int64) (map[int]bool, error)
}

// HopPlan is the realm deployment for one hop of one route.
type HopPlan struct {
	RouteID   int64
	RouteName string
	HopOrder  int
	Node      *model.Node

	// Listen is the port this hop accepts traffic on.
	Listen int
	// Remote is where this hop forwards to: the next hop's address, or the
	// landing target for the final hop.
	Remote string

	Config     string
	ConfigPath string
	UnitName   string
	Hash       string
}

// Plan is the full deployment for a route.
type Plan struct {
	Route *model.Route
	Hops  []HopPlan
}

// InstanceName is the realm instance identifier for a route, used in unit and
// file names. It is derived from the route name, which the API constrains to
// safe characters.
func InstanceName(routeName string) string {
	return "fluxlite-" + routeName
}

// ConfigPath is where a route's realm config lives on a node.
func ConfigPath(routeName string) string {
	return "/etc/fluxlite/realm/" + routeName + ".toml"
}

// LogPath is where a route's realm log lives on a node.
func LogPath(routeName string) string {
	return "/var/log/fluxlite/" + routeName + ".log"
}

var (
	ErrNoHops          = fmt.Errorf("route has no hops")
	ErrPortPoolEmpty   = fmt.Errorf("node has no free port in its pool")
	ErrEntryPortTaken  = fmt.Errorf("entry port is already claimed on the node")
	ErrEntryOutOfPool  = fmt.Errorf("entry port is outside the node's port pool")
	ErrUDPUnsupported  = fmt.Errorf("hop cannot pass UDP but the route requires it")
	ErrNodeNotProbed   = fmt.Errorf("node has not been probed yet")
)

// Allocate assigns a listen port to every hop of the route, honouring each
// node's pool and the ports already claimed by other routes.
//
// entryPort may be nil to let the planner choose. excludeRouteID lets an
// update reuse the ports the route already holds.
func Allocate(ctx context.Context, lookup NodeLookup, hops []model.RouteHop, protocol model.Protocol, entryPort *int, excludeRouteID *int64) ([]model.RouteHop, error) {
	if len(hops) == 0 {
		return nil, ErrNoHops
	}

	// reserved tracks ports claimed within this allocation so two hops landing
	// on the same node cannot be handed the same port.
	reserved := make(map[int64]map[int]bool)
	reserve := func(nodeID int64, port int) {
		if reserved[nodeID] == nil {
			reserved[nodeID] = make(map[int]bool)
		}
		reserved[nodeID][port] = true
	}

	out := make([]model.RouteHop, len(hops))
	copy(out, hops)

	for i := range out {
		node, err := lookup.NodeByID(ctx, out[i].NodeID)
		if err != nil {
			return nil, fmt.Errorf("hop %d: %w", i, err)
		}
		if !node.Probed() {
			return nil, fmt.Errorf("hop %d (%s): %w", i, node.Name, ErrNodeNotProbed)
		}
		if protocol.NeedsUDP() && node.UDPCapable != nil && !*node.UDPCapable {
			return nil, fmt.Errorf("hop %d (%s): %w", i, node.Name, ErrUDPUnsupported)
		}

		used, err := lookup.UsedPortsOnNode(ctx, node.ID, excludeRouteID)
		if err != nil {
			return nil, fmt.Errorf("hop %d (%s): %w", i, node.Name, err)
		}

		if i == 0 && entryPort != nil {
			p := *entryPort
			if p < node.PortStart || p > node.PortEnd {
				return nil, fmt.Errorf("hop 0 (%s) port %d: %w", node.Name, p, ErrEntryOutOfPool)
			}
			if used[p] || reserved[node.ID][p] {
				return nil, fmt.Errorf("hop 0 (%s) port %d: %w", node.Name, p, ErrEntryPortTaken)
			}
			out[i].RelayPort = p
			reserve(node.ID, p)
			continue
		}

		port, ok := firstFree(node, used, reserved[node.ID])
		if !ok {
			return nil, fmt.Errorf("hop %d (%s, pool %d-%d): %w",
				i, node.Name, node.PortStart, node.PortEnd, ErrPortPoolEmpty)
		}
		out[i].RelayPort = port
		reserve(node.ID, port)
	}
	return out, nil
}

func firstFree(node *model.Node, used, reserved map[int]bool) (int, bool) {
	for p := node.PortStart; p <= node.PortEnd; p++ {
		if !used[p] && !reserved[p] {
			return p, true
		}
	}
	return 0, false
}

// Build produces the per-hop realm deployment for a route whose hops already
// carry allocated ports.
func Build(ctx context.Context, lookup NodeLookup, route *model.Route) (*Plan, error) {
	if len(route.Hops) == 0 {
		return nil, ErrNoHops
	}

	nodes := make([]*model.Node, len(route.Hops))
	for i, h := range route.Hops {
		n, err := lookup.NodeByID(ctx, h.NodeID)
		if err != nil {
			return nil, fmt.Errorf("hop %d: %w", i, err)
		}
		nodes[i] = n
	}

	plan := &Plan{Route: route, Hops: make([]HopPlan, len(route.Hops))}
	for i, h := range route.Hops {
		remote := route.Target
		if i < len(route.Hops)-1 {
			next := route.Hops[i+1]
			remote = net.JoinHostPort(nodes[i+1].Host, strconv.Itoa(next.RelayPort))
		}

		cfg := renderConfig(route, h.RelayPort, remote)
		plan.Hops[i] = HopPlan{
			RouteID:    route.ID,
			RouteName:  route.Name,
			HopOrder:   h.HopOrder,
			Node:       nodes[i],
			Listen:     h.RelayPort,
			Remote:     remote,
			Config:     cfg,
			ConfigPath: ConfigPath(route.Name),
			UnitName:   InstanceName(route.Name),
			Hash:       hashConfig(cfg),
		}
	}
	return plan, nil
}

// renderConfig emits the realm TOML for one hop.
func renderConfig(route *model.Route, listen int, remote string) string {
	var b strings.Builder
	b.WriteString("# Managed by fluxlite. Manual edits are overwritten on apply.\n")
	fmt.Fprintf(&b, "# route: %s\n\n", route.Name)

	b.WriteString("[log]\n")
	b.WriteString("level = \"warn\"\n")
	fmt.Fprintf(&b, "output = %q\n\n", LogPath(route.Name))

	b.WriteString("[network]\n")
	b.WriteString("no_tcp = false\n")
	fmt.Fprintf(&b, "use_udp = %t\n\n", route.Protocol.NeedsUDP())

	b.WriteString("[[endpoints]]\n")
	fmt.Fprintf(&b, "listen = \"0.0.0.0:%d\"\n", listen)
	fmt.Fprintf(&b, "remote = %q\n", remote)
	return b.String()
}

// hashConfig fingerprints a rendered config so the applier can skip nodes
// whose configuration has not changed. realm cannot reload, so an
// unnecessary write would mean an unnecessary connection drop.
func hashConfig(cfg string) string {
	sum := sha256.Sum256([]byte(cfg))
	return hex.EncodeToString(sum[:])
}
