// Package model defines the domain types shared across fluxlite.
package model

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MaxDisplayNameLen bounds a user-supplied name in runes, so a Chinese name
// gets the same generous allowance as an ASCII one.
const MaxDisplayNameLen = 64

var (
	ErrNameEmpty   = errors.New("名称不能为空")
	ErrNameTooLong = fmt.Errorf("名称不能超过 %d 个字符", MaxDisplayNameLen)
	ErrNameControl = errors.New("名称不能包含控制字符")
)

// ValidateDisplayName accepts any human-readable label: Chinese, emoji,
// punctuation. It rejects only what would break something downstream —
// emptiness, absurd length, and control characters, which can smuggle ANSI
// escape sequences into logs and terminals.
func ValidateDisplayName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ErrNameEmpty
	}
	if utf8.RuneCountInString(trimmed) > MaxDisplayNameLen {
		return ErrNameTooLong
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return ErrNameControl
		}
	}
	return nil
}

// AuthType selects how fluxlite authenticates to a node over SSH.
type AuthType string

const (
	AuthKey      AuthType = "key"
	AuthPassword AuthType = "password"
)

func (a AuthType) Valid() bool {
	return a == AuthKey || a == AuthPassword
}

// InitSystem is the service manager present on a node. It decides how realm
// units are installed and controlled.
type InitSystem string

const (
	InitSystemd InitSystem = "systemd"
	InitOpenRC  InitSystem = "openrc"
	InitUnknown InitSystem = ""
)

func (i InitSystem) Valid() bool {
	return i == InitSystemd || i == InitOpenRC
}

// NodeStatus is the last known reachability of a node.
type NodeStatus string

const (
	StatusUnknown NodeStatus = "unknown"
	StatusOnline  NodeStatus = "online"
	StatusOffline NodeStatus = "offline"
)

// Protocol is the transport a route carries.
type Protocol string

const (
	ProtocolTCP    Protocol = "tcp"
	ProtocolTCPUDP Protocol = "tcp+udp"
)

func (p Protocol) Valid() bool {
	return p == ProtocolTCP || p == ProtocolTCPUDP
}

// NeedsUDP reports whether every hop of a route with this protocol must be
// able to pass UDP.
func (p Protocol) NeedsUDP() bool {
	return p == ProtocolTCPUDP
}

// Node is a machine that can carry traffic and that fluxlite manages over SSH.
type Node struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Host     string   `json:"host"`
	SSHPort  int      `json:"ssh_port"`
	SSHUser  string   `json:"ssh_user"`
	AuthType AuthType `json:"auth_type"`

	// AuthSecret holds the private key PEM or the password, encrypted at rest.
	// It is never serialised to API responses.
	AuthSecret []byte `json:"-"`

	// ViaNodeID is the jump host used to reach this node. Nodes behind a NAT
	// that only accept traffic from a specific peer need this; nil means direct.
	ViaNodeID *int64 `json:"via_node_id"`

	// PortStart and PortEnd bound the ports fluxlite may allocate on this node.
	// NAT hosts typically expose only a narrow forwarded range.
	PortStart int `json:"port_start"`
	PortEnd   int `json:"port_end"`

	// HostKey is the node's SSH public key in authorized_keys form, recorded on
	// first connect and strictly verified afterwards. Without it a machine on
	// the path could impersonate the node and harvest root credentials.
	HostKey string `json:"host_key"`

	Arch       string     `json:"arch"`
	OSID       string     `json:"os_id"`
	InitSystem InitSystem `json:"init_system"`

	// UDPCapable is nil until probed. False means UDP does not survive the
	// path to this node, which disqualifies it from tcp+udp routes.
	UDPCapable *bool `json:"udp_capable"`

	// SkipUDPProbe suppresses the UDP reachability check for this node. Most
	// NAT hosts forward TCP only, so an operator who never intends to carry
	// UDP can skip a probe that costs a dozen seconds on every refresh.
	SkipUDPProbe bool `json:"skip_udp_probe"`

	// RealmVersion is the realm build currently installed, empty if absent.
	RealmVersion string `json:"realm_version"`

	Status    NodeStatus `json:"status"`
	LastSeen  *time.Time `json:"last_seen"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Addr returns the SSH dial address of the node.
func (n *Node) Addr() string {
	return net.JoinHostPort(n.Host, strconv.Itoa(n.SSHPort))
}

// PortPoolSize is the number of allocatable ports on the node.
func (n *Node) PortPoolSize() int {
	if n.PortEnd < n.PortStart {
		return 0
	}
	return n.PortEnd - n.PortStart + 1
}

// Probed reports whether the node has been through capability detection.
func (n *Node) Probed() bool {
	return n.InitSystem.Valid() && n.Arch != ""
}

var (
	ErrNodeNameEmpty = errors.New("node name must not be empty")
	ErrNodeHostEmpty = errors.New("node host must not be empty")
	ErrNodeUserEmpty = errors.New("node ssh user must not be empty")
	ErrNodeAuthType  = errors.New("node auth type must be key or password")
	ErrNodeSSHPort   = errors.New("node ssh port must be between 1 and 65535")
	ErrNodePortRange = errors.New("node port range must satisfy 1 <= start <= end <= 65535")
	ErrNodeSelfVia   = errors.New("node cannot use itself as a jump host")
)

// Validate checks the invariants that must hold before a node is persisted.
func (n *Node) Validate() error {
	if err := ValidateDisplayName(n.Name); err != nil {
		return err
	}
	if strings.TrimSpace(n.Host) == "" {
		return ErrNodeHostEmpty
	}
	if strings.TrimSpace(n.SSHUser) == "" {
		return ErrNodeUserEmpty
	}
	if !n.AuthType.Valid() {
		return ErrNodeAuthType
	}
	if n.SSHPort < 1 || n.SSHPort > 65535 {
		return ErrNodeSSHPort
	}
	if n.PortStart < 1 || n.PortEnd > 65535 || n.PortStart > n.PortEnd {
		return ErrNodePortRange
	}
	if n.ViaNodeID != nil && *n.ViaNodeID == n.ID && n.ID != 0 {
		return ErrNodeSelfVia
	}
	return nil
}

// Route is a forwarding chain: traffic enters at hop 0 and is relayed through
// each subsequent hop until the final hop dials Target.
type Route struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`

	// Slug is the ASCII-safe identifier used for systemd instance names, init
	// script names and config paths on the nodes. Name is free-form and may be
	// Chinese or contain punctuation; Slug is what actually touches the
	// filesystem and the service manager, and never changes once assigned.
	Slug string `json:"slug"`

	Target   string   `json:"target"`
	Protocol Protocol `json:"protocol"`

	// EntryPort is the port the first hop listens on: the address clients
	// connect to. It mirrors Hops[0].RelayPort, which is the stored value, so
	// that one uniqueness constraint covers entry and relay ports alike.
	EntryPort int `json:"entry_port"`

	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Hops is ordered by HopOrder ascending and is populated on read.
	Hops []RouteHop `json:"hops,omitempty"`
}

// RouteHop is one node in a route's chain. HopOrder 0 is the entry node.
type RouteHop struct {
	RouteID  int64 `json:"route_id"`
	HopOrder int   `json:"hop_order"`
	NodeID   int64 `json:"node_id"`

	// RelayPort is the port this hop listens on. For hop 0 it mirrors
	// Route.EntryPort; for later hops it is allocated from the node's pool.
	RelayPort int `json:"relay_port"`

	// LatencyMS is how long this hop took to reach what it forwards to — the
	// next hop, or the landing target for the final hop. It is nil until a
	// verification measures it, and nil is never shown as zero: an unmeasured
	// link and an instant one are not the same claim.
	LatencyMS *int `json:"latency_ms"`

	// LatencyAt records when that measurement was taken, so a stale number is
	// recognisable as one.
	LatencyAt *time.Time `json:"latency_at"`

	// Running is the relay's state as of the last sample, nil when it has
	// never been checked. Nil is not false: "not yet known" and "confirmed
	// down" call for different reactions.
	Running *bool `json:"running"`

	// CheckedAt dates the liveness sample. A sample that stopped refreshing
	// describes a node that stopped answering, not a healthy relay.
	CheckedAt *time.Time `json:"checked_at"`
}

var (
	ErrRouteNameEmpty  = errors.New("route name must not be empty")
	ErrRouteSlugEmpty  = errors.New("route slug must not be empty")
	ErrRouteTargetBad  = errors.New("route target must be host:port")
	ErrRouteProtocol   = errors.New("route protocol must be tcp or tcp+udp")
	ErrRouteEntryPort  = errors.New("route entry port must be between 1 and 65535")
	ErrRouteTooFewHops = errors.New("route must have at least one hop")
	ErrRouteHopRepeat  = errors.New("route must not visit the same node twice in a row")
)

// Validate checks the invariants that must hold before a route is persisted.
func (r *Route) Validate() error {
	if err := ValidateDisplayName(r.Name); err != nil {
		return err
	}
	if r.Slug == "" {
		return ErrRouteSlugEmpty
	}
	if err := ValidateTarget(r.Target); err != nil {
		return err
	}
	if !r.Protocol.Valid() {
		return ErrRouteProtocol
	}
	if r.EntryPort < 1 || r.EntryPort > 65535 {
		return ErrRouteEntryPort
	}
	if len(r.Hops) < 1 {
		return ErrRouteTooFewHops
	}
	// Chaining a node to itself would make realm dial its own listener.
	for i := 1; i < len(r.Hops); i++ {
		if r.Hops[i].NodeID == r.Hops[i-1].NodeID {
			return ErrRouteHopRepeat
		}
	}
	return nil
}

// ValidateTarget checks that a landing address is a usable host:port pair.
func ValidateTarget(target string) error {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(target))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRouteTargetBad, err)
	}
	if strings.TrimSpace(host) == "" {
		return ErrRouteTargetBad
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return ErrRouteTargetBad
	}
	return nil
}

// AuditLog is an append-only record of an operator action.
type AuditLog struct {
	ID     int64     `json:"id"`
	TS     time.Time `json:"ts"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail"`
	IP     string    `json:"ip"`
}
