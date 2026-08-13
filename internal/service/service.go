// Package service orchestrates the storage, planning, deployment and
// verification layers into the operations the API exposes.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kukumi1/fluxlite/internal/applier"
	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/planner"
	"github.com/kukumi1/fluxlite/internal/prober"
	"github.com/kukumi1/fluxlite/internal/sshx"
	"github.com/kukumi1/fluxlite/internal/store"
	"github.com/kukumi1/fluxlite/internal/verifier"
)

var (
	ErrNodeInUse      = errors.New("node is still referenced")
	ErrProbeFirst     = errors.New("node must be probed before it can carry a route")
	ErrRouteNeedsHops = errors.New("route needs at least one hop")
)

// Service is the application core.
type Service struct {
	store    *store.Store
	sealer   *cryptox.Sealer
	pool     *sshx.Pool
	applier  *applier.Applier
	verifier *verifier.Verifier
	realm    applier.RealmSource
	log      *slog.Logger
}

func New(st *store.Store, sealer *cryptox.Sealer, pool *sshx.Pool, ap *applier.Applier, vf *verifier.Verifier, realm applier.RealmSource, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: st, sealer: sealer, pool: pool, applier: ap, verifier: vf, realm: realm, log: log}
}

// NodeInput is the client-supplied description of a node.
type NodeInput struct {
	Name      string         `json:"name"`
	Host      string         `json:"host"`
	SSHPort   int            `json:"ssh_port"`
	SSHUser   string         `json:"ssh_user"`
	AuthType  model.AuthType `json:"auth_type"`
	Secret    string         `json:"secret"`
	ViaNodeID *int64         `json:"via_node_id"`
	PortStart int            `json:"port_start"`
	PortEnd   int            `json:"port_end"`
	SkipUDP   bool           `json:"skip_udp_probe"`
}

// CreateNode stores a node with its credential encrypted at rest.
func (s *Service) CreateNode(ctx context.Context, in NodeInput) (*model.Node, error) {
	if err := model.ValidateDisplayName(in.Name); err != nil {
		return nil, err
	}
	if in.Secret == "" {
		return nil, errors.New("credential must not be empty")
	}
	sealed, err := s.sealer.Seal([]byte(in.Secret))
	if err != nil {
		return nil, fmt.Errorf("seal credential: %w", err)
	}

	node := &model.Node{
		Name:         in.Name,
		Host:         in.Host,
		SSHPort:      in.SSHPort,
		SSHUser:      in.SSHUser,
		AuthType:     in.AuthType,
		AuthSecret:   sealed,
		ViaNodeID:    in.ViaNodeID,
		PortStart:    in.PortStart,
		PortEnd:      in.PortEnd,
		SkipUDPProbe: in.SkipUDP,
		Status:       model.StatusUnknown,
	}
	if err := s.store.CreateNode(ctx, node); err != nil {
		return nil, err
	}
	return node, nil
}

// UpdateNode changes a node's connection details. An empty secret keeps the
// stored credential.
func (s *Service) UpdateNode(ctx context.Context, id int64, in NodeInput) (*model.Node, error) {
	node, err := s.store.NodeByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := model.ValidateDisplayName(in.Name); err != nil {
		return nil, err
	}
	if in.ViaNodeID != nil && *in.ViaNodeID == id {
		return nil, model.ErrNodeSelfVia
	}

	// A changed address invalidates the pinned host key: the operator is
	// pointing at a different machine, so trust must be established again.
	if node.Host != in.Host || node.SSHPort != in.SSHPort {
		node.HostKey = ""
	}

	node.Name = in.Name
	node.Host = in.Host
	node.SSHPort = in.SSHPort
	node.SSHUser = in.SSHUser
	node.AuthType = in.AuthType
	node.ViaNodeID = in.ViaNodeID
	node.PortStart = in.PortStart
	node.PortEnd = in.PortEnd
	node.SkipUDPProbe = in.SkipUDP

	if in.Secret != "" {
		sealed, err := s.sealer.Seal([]byte(in.Secret))
		if err != nil {
			return nil, fmt.Errorf("seal credential: %w", err)
		}
		node.AuthSecret = sealed
	}

	if err := s.store.UpdateNode(ctx, node); err != nil {
		return nil, err
	}
	return node, nil
}

// DeleteNode removes a node once nothing references it.
func (s *Service) DeleteNode(ctx context.Context, id int64) error {
	routes, err := s.store.RoutesOnNode(ctx, id)
	if err != nil {
		return err
	}
	if len(routes) > 0 {
		return fmt.Errorf("%w by %d route(s)", ErrNodeInUse, len(routes))
	}
	dependents, err := s.store.NodesReferencing(ctx, id)
	if err != nil {
		return err
	}
	if len(dependents) > 0 {
		return fmt.Errorf("%w as jump host by %d node(s)", ErrNodeInUse, len(dependents))
	}
	return s.store.DeleteNode(ctx, id)
}

// InstallRealm puts the forwarding kernel on a node ahead of any route, and
// records the version so the node list stops reporting it as absent.
func (s *Service) InstallRealm(ctx context.Context, id int64) (string, error) {
	node, err := s.store.NodeByID(ctx, id)
	if err != nil {
		return "", err
	}
	if node.Arch == "" {
		return "", ErrProbeFirst
	}
	if _, err := s.applier.InstallRealm(ctx, node); err != nil {
		return "", err
	}
	node.RealmVersion = applier.RealmVersion
	if err := s.store.UpdateNode(ctx, node); err != nil {
		return "", err
	}
	return node.RealmVersion, nil
}

// ProbeResult reports what a probe learned.
type ProbeResult struct {
	Facts *prober.Facts     `json:"facts"`
	UDP   *prober.UDPResult `json:"udp"`
}

// ProbeNode connects to a node, records its capabilities, and checks whether
// UDP survives the path to it.
func (s *Service) ProbeNode(ctx context.Context, id int64) (*ProbeResult, error) {
	node, err := s.store.NodeByID(ctx, id)
	if err != nil {
		return nil, err
	}

	client, err := s.pool.Get(ctx, node)
	if err != nil {
		now := time.Now().UTC()
		if serr := s.store.SetNodeStatus(ctx, id, model.StatusOffline, &now); serr != nil {
			return nil, fmt.Errorf("connect failed (%v) and status update failed: %w", err, serr)
		}
		return nil, fmt.Errorf("connect to %s: %w", node.Name, err)
	}

	facts, err := prober.Probe(ctx, client.Client)
	if err != nil {
		return nil, err
	}

	node.Arch = facts.Arch
	node.OSID = facts.OSID
	node.InitSystem = facts.InitSystem
	node.RealmVersion = facts.RealmVersion
	node.Status = model.StatusOnline
	now := time.Now().UTC()
	node.LastSeen = &now

	udp := s.probeUDP(ctx, node)
	if udp != nil && udp.Supported != nil {
		node.UDPCapable = udp.Supported
	}

	if err := s.store.UpdateNode(ctx, node); err != nil {
		return nil, err
	}
	return &ProbeResult{Facts: facts, UDP: udp}, nil
}

// probeUDP tests whether UDP survives the path to the node.
//
// The datagrams are aimed at the node's configured ingress address, which is
// where relay traffic actually lands. A NAT host egresses from a different
// address than it ingresses on, so asking the node for its own public IP
// would test a path no traffic ever takes.
func (s *Service) probeUDP(ctx context.Context, node *model.Node) *prober.UDPResult {
	if node.SkipUDPProbe {
		return &prober.UDPResult{Detail: "已按节点设置跳过 UDP 检测"}
	}

	target, err := s.pool.Get(ctx, node)
	if err != nil {
		return &prober.UDPResult{Detail: "target unreachable: " + err.Error()}
	}

	source, why := s.udpVantagePoint(ctx, node)
	if source == nil {
		return &prober.UDPResult{Detail: why}
	}

	res, err := prober.ProbeUDP(ctx, target.Client, source.Client, node.Host, node.PortEnd)
	if err != nil {
		return &prober.UDPResult{Detail: "probe error: " + err.Error()}
	}
	return res
}

// udpVantagePoint picks a machine to send the external datagrams from.
//
// The jump host is preferred because it is the direction real traffic arrives
// from. Otherwise any other reachable node will do — what matters is only that
// the packets cross the public network rather than loop back inside the target.
func (s *Service) udpVantagePoint(ctx context.Context, node *model.Node) (*sshx.Client, string) {
	if node.ViaNodeID != nil {
		if via, err := s.store.NodeByID(ctx, *node.ViaNodeID); err == nil {
			if c, err := s.pool.Get(ctx, via); err == nil {
				return c, ""
			}
		}
	}

	others, err := s.store.ListNodes(ctx)
	if err != nil {
		return nil, "无法枚举可用的发送点：" + err.Error()
	}
	for _, candidate := range others {
		if candidate.ID == node.ID || candidate.Host == node.Host {
			continue
		}
		if c, err := s.pool.Get(ctx, candidate); err == nil {
			return c, ""
		}
	}
	return nil, "没有其他可用节点作为发送点，无法判定 UDP 是否可达（至少需要两台在线节点）"
}

// RouteInput is the client-supplied description of a route.
type RouteInput struct {
	Name      string         `json:"name"`
	Target    string         `json:"target"`
	Protocol  model.Protocol `json:"protocol"`
	NodeIDs   []int64        `json:"node_ids"`
	EntryPort *int           `json:"entry_port"`
	Enabled   bool           `json:"enabled"`
}

// CreateRoute allocates ports for every hop and persists the route. It does
// not deploy; call ApplyRoute for that.
func (s *Service) CreateRoute(ctx context.Context, in RouteInput) (*model.Route, error) {
	if err := model.ValidateDisplayName(in.Name); err != nil {
		return nil, err
	}
	if len(in.NodeIDs) == 0 {
		return nil, ErrRouteNeedsHops
	}

	slug, err := makeSlug(ctx, s.store, in.Name)
	if err != nil {
		return nil, err
	}

	hops := make([]model.RouteHop, len(in.NodeIDs))
	for i, nodeID := range in.NodeIDs {
		hops[i] = model.RouteHop{HopOrder: i, NodeID: nodeID}
	}

	// Allocation consults the live listener list on each node, not just
	// fluxlite's own bookkeeping, so a wide port pool cannot hand out a port
	// that sshd or another service already holds.
	allocated, err := planner.Allocate(ctx, s.newLiveLookup(nil), hops, in.Protocol, in.EntryPort, nil)
	if err != nil {
		return nil, err
	}

	route := &model.Route{
		Name:     in.Name,
		Slug:     slug,
		Target:   in.Target,
		Protocol: in.Protocol,
		Enabled:  in.Enabled,
		Hops:     allocated,
	}
	if len(allocated) > 0 {
		route.EntryPort = allocated[0].RelayPort
	}
	if err := s.store.CreateRoute(ctx, route); err != nil {
		return nil, err
	}
	return route, nil
}

// UpdateRoute re-plans a route, reusing its own ports where possible.
func (s *Service) UpdateRoute(ctx context.Context, id int64, in RouteInput) (*model.Route, error) {
	route, err := s.store.RouteByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := model.ValidateDisplayName(in.Name); err != nil {
		return nil, err
	}
	if len(in.NodeIDs) == 0 {
		return nil, ErrRouteNeedsHops
	}

	previous := make([]model.RouteHop, len(route.Hops))
	copy(previous, route.Hops)

	hops := make([]model.RouteHop, len(in.NodeIDs))
	for i, nodeID := range in.NodeIDs {
		hops[i] = model.RouteHop{RouteID: id, HopOrder: i, NodeID: nodeID}
	}
	allocated, err := planner.Allocate(ctx, s.newLiveLookup(&id), hops, in.Protocol, in.EntryPort, &id)
	if err != nil {
		return nil, err
	}

	route.Name = in.Name
	route.Target = in.Target
	route.Protocol = in.Protocol
	route.Enabled = in.Enabled
	route.Hops = allocated
	if len(allocated) > 0 {
		route.EntryPort = allocated[0].RelayPort
	}
	if err := s.store.UpdateRoute(ctx, route); err != nil {
		return nil, err
	}

	// Nodes dropped from the chain keep running a relay nobody routes to
	// until their deployment is torn down.
	s.removeOrphanedHops(ctx, previous, allocated, route.Slug)
	return route, nil
}

// removeOrphanedHops tears down deployments on nodes no longer in the chain.
// Failures are tolerated: the route itself is already updated, and a stale
// relay on an unreachable node is reported by the next reconcile rather than
// blocking the operator here.
func (s *Service) removeOrphanedHops(ctx context.Context, previous, current []model.RouteHop, slug string) {
	inUse := make(map[int64]bool, len(current))
	for _, h := range current {
		inUse[h.NodeID] = true
	}
	for _, h := range previous {
		if inUse[h.NodeID] {
			continue
		}
		node, err := s.store.NodeByID(ctx, h.NodeID)
		if err != nil {
			continue
		}
		_ = s.applier.Remove(ctx, node, slug)
	}
}

// ApplyRoute deploys a route to every hop.
func (s *Service) ApplyRoute(ctx context.Context, id int64) (*applier.Result, error) {
	route, err := s.store.RouteByID(ctx, id)
	if err != nil {
		return nil, err
	}
	plan, err := planner.Build(ctx, s.store, route)
	if err != nil {
		return nil, err
	}
	result, err := s.applier.Apply(ctx, plan)
	if err != nil {
		return nil, err
	}

	// The applier already confirmed each service stayed up, so the operator
	// should not have to wait for the next sample to see it. Only hops the
	// applier actually reached are recorded; a skipped hop is still unknown.
	for _, hop := range result.Hops {
		if hop.Action == "skipped" || hop.Action == "unreachable" {
			continue
		}
		if serr := s.store.SetHopRunning(ctx, id, hop.HopOrder, hop.Error == ""); serr != nil {
			s.log.Warn("record hop liveness after apply", "route", route.Name, "error", serr)
		}
		// Rebuilt counters start from zero. Left in place, the stored baseline
		// would make the next reading look like a counter reset and credit the
		// route with the whole of it a second time.
		if hop.Accounting == applier.AcctRebuilt {
			if serr := s.store.ResetTrafficBaseline(ctx, id); serr != nil {
				s.log.Warn("reset traffic baseline after apply", "route", route.Name, "error", serr)
			}
		}
	}
	return result, nil
}

// VerifyRoute proves whether a deployed route actually carries traffic.
func (s *Service) VerifyRoute(ctx context.Context, id int64) (*verifier.Report, error) {
	route, err := s.store.RouteByID(ctx, id)
	if err != nil {
		return nil, err
	}
	plan, err := planner.Build(ctx, s.store, route)
	if err != nil {
		return nil, err
	}
	report, err := s.verifier.Verify(ctx, plan)
	if err != nil {
		return nil, err
	}

	latencies := make(map[int]int)
	for _, check := range report.Checks {
		if check.HopOrder != nil && check.LatencyMS != nil {
			latencies[*check.HopOrder] = *check.LatencyMS
		}
	}
	// The report is the operator's answer; failing to cache it must not turn a
	// successful verification into an error.
	if err := s.store.SetHopLatencies(ctx, id, latencies); err != nil {
		s.log.Warn("record hop latencies", "route", route.Name, "error", err)
	}
	return report, nil
}

// SampleRoute refreshes the runtime facts shown on the route list: whether
// each relay is running, and how long each hop takes to reach what it forwards
// to. Both come from the same pass over the chain, since asking twice would
// double the sessions opened on every node.
//
// It is deliberately cheaper than a verification: no marker, no capture, and
// therefore no proof of delivery. Proof stays where an operator asks for it.
func (s *Service) SampleRoute(ctx context.Context, id int64) error {
	route, err := s.store.RouteByID(ctx, id)
	if err != nil {
		return err
	}
	plan, err := planner.Build(ctx, s.store, route)
	if err != nil {
		return err
	}

	// Liveness is recorded per hop as it is learned. A node that has gone away
	// must not prevent the hops behind it from reporting.
	var firstErr error
	for _, hop := range plan.Hops {
		running, err := s.applier.Status(ctx, hop.Node, route.Slug)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", hop.Node.Name, err)
			}
			continue
		}
		if serr := s.store.SetHopRunning(ctx, id, hop.HopOrder, running); serr != nil {
			return serr
		}
	}

	latencies, err := s.verifier.MeasureLatencies(ctx, plan)
	if err != nil {
		return err
	}
	if err := s.store.SetHopLatencies(ctx, id, latencies); err != nil {
		return err
	}
	return firstErr
}

// RouteLeftover names a node whose relay outlived the route's deletion.
type RouteLeftover struct {
	NodeID   int64  `json:"node_id"`
	NodeName string `json:"node_name"`
	Reason   string `json:"reason"`
}

// DeleteRoute tears the route down on every hop it can still reach, then
// removes it.
//
// Deletion always succeeds. Cleanup runs on a best effort basis because the
// commonest reason to delete a route is that one of its machines is gone for
// good — letting an unreachable node veto the delete strands the route, and
// with it the node record the route refers to, with no way out from the panel.
//
// What cleanup could not do is returned rather than swallowed: a relay left
// running on a machine that later comes back is still forwarding traffic the
// panel no longer knows about.
func (s *Service) DeleteRoute(ctx context.Context, id int64) ([]RouteLeftover, error) {
	route, err := s.store.RouteByID(ctx, id)
	if err != nil {
		return nil, err
	}

	var leftovers []RouteLeftover
	for _, hop := range route.Hops {
		node, err := s.store.NodeByID(ctx, hop.NodeID)
		if err != nil {
			continue
		}
		// Dialling a node already known to be down only makes the caller wait
		// out the SSH timeout to learn what the panel had already recorded.
		if node.Status == model.StatusOffline {
			leftovers = append(leftovers, RouteLeftover{
				NodeID: node.ID, NodeName: node.Name,
				Reason: "node is offline, cleanup was not attempted",
			})
			continue
		}
		if rerr := s.applier.Remove(ctx, node, route.Slug); rerr != nil {
			leftovers = append(leftovers, RouteLeftover{
				NodeID: node.ID, NodeName: node.Name, Reason: rerr.Error(),
			})
		}
	}

	if err := s.store.DeleteRoute(ctx, id); err != nil {
		return nil, err
	}
	for _, l := range leftovers {
		s.log.Warn("route deleted but its relay could not be removed",
			"route", route.Name, "slug", route.Slug, "node", l.NodeName, "reason", l.Reason)
	}
	return leftovers, nil
}

// StopRoute disables and stops a route without deleting it.
func (s *Service) StopRoute(ctx context.Context, id int64) error {
	route, err := s.store.RouteByID(ctx, id)
	if err != nil {
		return err
	}
	for _, hop := range route.Hops {
		node, err := s.store.NodeByID(ctx, hop.NodeID)
		if err != nil {
			continue
		}
		if rerr := s.applier.Remove(ctx, node, route.Slug); rerr != nil {
			return fmt.Errorf("stop route on %s: %w", node.Name, rerr)
		}
	}
	if err := s.store.ClearHopRunning(ctx, id); err != nil {
		return err
	}
	return s.store.SetRouteEnabled(ctx, id, false)
}

// RouteStatus reports where each hop of a route currently stands.
type RouteStatus struct {
	RouteID int64            `json:"route_id"`
	Name    string           `json:"name"`
	Hops    []HopStatusEntry `json:"hops"`
}

// HopStatusEntry is one hop's runtime state.
type HopStatusEntry struct {
	NodeID   int64  `json:"node_id"`
	NodeName string `json:"node_name"`
	HopOrder int    `json:"hop_order"`
	Listen   int    `json:"listen"`
	// Running is nil until the background sampler has reached this hop. Nil is
	// not false: a hop nobody has checked yet must not be drawn as down.
	Running   *bool      `json:"running"`
	CheckedAt *time.Time `json:"checked_at"`
	Error     string     `json:"error,omitempty"`
}

// RouteStatuses reports runtime state for every route.
//
// It answers from what the background sampler recorded rather than asking the
// nodes. Asking meant an SSH session per hop on every call, which is why this
// could not be polled; served from the database the route list can refresh
// itself as often as it likes.
func (s *Service) RouteStatuses(ctx context.Context) ([]RouteStatus, error) {
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]RouteStatus, 0, len(routes))
	for _, r := range routes {
		status := RouteStatus{RouteID: r.ID, Name: r.Name}
		for _, hop := range r.Hops {
			entry := HopStatusEntry{
				NodeID:    hop.NodeID,
				HopOrder:  hop.HopOrder,
				Listen:    hop.RelayPort,
				Running:   hop.Running,
				CheckedAt: hop.CheckedAt,
			}
			node, err := s.store.NodeByID(ctx, hop.NodeID)
			if err != nil {
				entry.Error = err.Error()
			} else {
				entry.NodeName = node.Name
			}
			status.Hops = append(status.Hops, entry)
		}
		out = append(out, status)
	}
	return out, nil
}

// Store exposes the underlying store for read-only handlers.
func (s *Service) Store() *store.Store { return s.store }
