package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kukumi1/fluxlite/internal/cryptox"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/store"
)

// EnrollTTL bounds how long a generated command stays usable.
const EnrollTTL = 60 * time.Minute

// enrollProbeTimeout bounds the reachability check run at the end of an
// enrollment.
//
// The number is chosen against the reverse proxy in front of the panel rather
// than against the probe: proxies commonly abandon an origin request somewhere
// between sixty and a hundred seconds, and Cloudflare's limit is a hundred. The
// enrollment is already committed by the time the probe starts, so a proxy
// timing the request out costs the operator the verdict and tells them, quite
// wrongly, that nothing was registered. A successful probe takes about ten
// seconds; this leaves room for one failed dial and still answers well inside
// any of those limits.
const enrollProbeTimeout = 45 * time.Second

var (
	ErrEnrollTokenInvalid = errors.New("enrollment token is invalid, expired or already used")
	ErrEnrollNameTaken    = errors.New("a node with this name already exists")
)

// EnrollRequest describes the node an operator is about to install.
//
// Host and SSHPort are supplied by the operator rather than detected on the
// machine: a NAT host reaches the internet from one address and accepts SSH on
// a different forwarded one, so self-detection would record an address the
// panel cannot dial.
type EnrollRequest struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	SSHPort   int    `json:"ssh_port"`
	SSHUser   string `json:"ssh_user"`
	PortStart int    `json:"port_start"`
	PortEnd   int    `json:"port_end"`
	ViaNodeID *int64 `json:"via_node_id"`
	SkipUDP   bool   `json:"skip_udp_probe"`
}

// EnrollTicket is what the panel shows the operator.
type EnrollTicket struct {
	Token     string    `json:"token"`
	Command   string    `json:"command"`
	ExpiresAt time.Time `json:"expires_at"`
}

// CreateEnrollTicket generates a key pair and a one-shot token, returning the
// command to paste onto the new machine.
func (s *Service) CreateEnrollTicket(ctx context.Context, baseURL string, req EnrollRequest) (*EnrollTicket, error) {
	if _, err := s.store.NodeByName(ctx, req.Name); err == nil {
		return nil, ErrEnrollNameTaken
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return s.issueEnrollTicket(ctx, baseURL, req, nil)
}

// CreateReenrollTicket issues a ticket that re-credentials an existing node.
//
// A reinstalled machine presents a new host key and has never heard of the
// panel's key, so the panel cannot dial it and cannot fix that over SSH — the
// operator has to run the installer again. Deleting and re-adding the node
// would do it, except DeleteNode refuses while any route references the node,
// so the real cost is tearing down every route and rebuilding it by hand.
//
// The target is taken from the node record rather than from the caller. Nothing
// about the machine's identity is re-entered, so nothing about it can be
// mistyped, and the normal enrolment path keeps no way to aim at an existing
// node.
func (s *Service) CreateReenrollTicket(ctx context.Context, baseURL string, nodeID int64) (*EnrollTicket, error) {
	node, err := s.store.NodeByID(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return s.issueEnrollTicket(ctx, baseURL, EnrollRequest{
		Name:      node.Name,
		Host:      node.Host,
		SSHPort:   node.SSHPort,
		SSHUser:   node.SSHUser,
		PortStart: node.PortStart,
		PortEnd:   node.PortEnd,
		ViaNodeID: node.ViaNodeID,
		SkipUDP:   node.SkipUDPProbe,
	}, &node.ID)
}

func (s *Service) issueEnrollTicket(ctx context.Context, baseURL string, req EnrollRequest, target *int64) (*EnrollTicket, error) {
	if err := model.ValidateDisplayName(req.Name); err != nil {
		return nil, err
	}
	if req.Host == "" {
		return nil, model.ErrNodeHostEmpty
	}
	if req.SSHPort < 1 || req.SSHPort > 65535 {
		return nil, model.ErrNodeSSHPort
	}
	if req.SSHUser == "" {
		return nil, model.ErrNodeUserEmpty
	}
	if req.PortStart < 1 || req.PortEnd > 65535 || req.PortStart > req.PortEnd {
		return nil, model.ErrNodePortRange
	}

	pair, err := cryptox.GenerateSSHKeyPair("fluxlite-" + req.Name)
	if err != nil {
		return nil, err
	}
	sealed, err := s.sealer.Seal(pair.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("seal node key: %w", err)
	}

	token, err := cryptox.RandomToken(24)
	if err != nil {
		return nil, err
	}

	ticket := &store.EnrollToken{
		Token:         token,
		Name:          req.Name,
		Host:          req.Host,
		SSHPort:       req.SSHPort,
		SSHUser:       req.SSHUser,
		PortStart:     req.PortStart,
		PortEnd:       req.PortEnd,
		ViaNodeID:     req.ViaNodeID,
		SkipUDPProbe:  req.SkipUDP,
		PrivateKey:    sealed,
		AuthorizedKey: pair.AuthorizedKey,
		ExpiresAt:     time.Now().UTC().Add(EnrollTTL),
		TargetNodeID:  target,
	}
	if err := s.store.CreateEnrollToken(ctx, ticket); err != nil {
		return nil, err
	}

	return &EnrollTicket{
		Token:     token,
		Command:   fmt.Sprintf("curl -fsSL %s/enroll.sh | sh -s -- %s %s", baseURL, baseURL, token),
		ExpiresAt: ticket.ExpiresAt,
	}, nil
}

// EnrollReport is what the install script sends back.
type EnrollReport struct {
	Token        string `json:"token"`
	Arch         string `json:"arch"`
	OSID         string `json:"os_id"`
	InitSystem   string `json:"init_system"`
	RealmVersion string `json:"realm_version"`
	Hostname     string `json:"hostname"`
}

// EnrollOutcome is returned to the script so the operator sees the verdict in
// the same terminal where they ran the command.
type EnrollOutcome struct {
	NodeID    int64  `json:"node_id"`
	Name      string `json:"name"`
	Verified  bool   `json:"verified"`
	Detail    string `json:"detail"`
	UDPStatus string `json:"udp_status"`

	// Reinstalled distinguishes a node that was re-credentialled in place from
	// a newly created one, so the terminal can say which happened and the audit
	// trail can record that a host key pin was reset.
	Reinstalled bool `json:"reinstalled"`
}

// AuthorizedKeyForToken returns the public key a pending enrollment should
// install. It deliberately reveals nothing else about the node.
func (s *Service) AuthorizedKeyForToken(ctx context.Context, token string) (string, error) {
	t, err := s.store.EnrollTokenByValue(ctx, token)
	if err != nil {
		return "", ErrEnrollTokenInvalid
	}
	if !t.Usable(time.Now().UTC()) {
		return "", ErrEnrollTokenInvalid
	}
	return t.AuthorizedKey, nil
}

// CompleteEnroll turns a reporting script into a managed node, then
// immediately dials the node to confirm the panel can actually reach it.
func (s *Service) CompleteEnroll(ctx context.Context, report EnrollReport) (*EnrollOutcome, error) {
	t, err := s.store.EnrollTokenByValue(ctx, report.Token)
	if err != nil {
		return nil, ErrEnrollTokenInvalid
	}
	if !t.Usable(time.Now().UTC()) {
		return nil, ErrEnrollTokenInvalid
	}

	init := model.InitSystem(report.InitSystem)
	if !init.Valid() {
		return nil, fmt.Errorf("unsupported init system %q reported by the node", report.InitSystem)
	}

	node, reinstalled, err := s.applyEnrollment(ctx, t, report, init)
	if err != nil {
		return nil, err
	}
	if err := s.store.MarkEnrollTokenUsed(ctx, report.Token, node.ID); err != nil {
		return nil, err
	}

	outcome := &EnrollOutcome{NodeID: node.ID, Name: node.Name, Reinstalled: reinstalled}

	// Registering a node the panel cannot dial is worse than useless: it looks
	// healthy in the list and fails at the worst moment. Probe now and report
	// the verdict while the operator is still at the terminal.
	//
	// The probe is detached from the caller's cancellation on purpose. Every
	// write above has already landed and the token is spent, so from here on
	// the only question is whether the panel's record of the node ends up
	// matching reality. Inheriting the request context means an operator who
	// hits Ctrl-C — or a proxy that gives up on the request — also cancels the
	// status write, leaving the node recorded as whatever it was before.
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), enrollProbeTimeout)
	defer cancel()

	result, err := s.ProbeNode(probeCtx, node.ID)
	if err != nil {
		lead := "节点已登记，但面板拨号失败："
		if reinstalled {
			lead = "节点凭据已更新，但面板拨号失败："
		}
		outcome.Detail = lead + err.Error()
		return outcome, nil
	}

	outcome.Verified = true
	outcome.Detail = fmt.Sprintf("面板已成功连接：%s/%s %s，realm %s",
		result.Facts.OSID, result.Facts.Arch, result.Facts.InitSystem,
		orNone(result.Facts.RealmVersion))

	switch {
	case result.UDP == nil || result.UDP.Supported == nil:
		outcome.UDPStatus = "未知"
	case *result.UDP.Supported:
		outcome.UDPStatus = "通"
	default:
		outcome.UDPStatus = "不通"
	}
	return outcome, nil
}

// applyEnrollment either creates the node the ticket describes or, when the
// ticket names one, re-credentials that node in place.
//
// Re-credentialling keeps the node's id, and with it every route hop, port
// allocation and jump-host reference pointing at it. Only what the reinstall
// actually invalidated is replaced.
//
// Clearing HostKey is the one dangerous line here. That pin is what stops a
// machine on the path from impersonating a node and collecting the panel's
// root session, so it is never dropped on a whim — only when an operator has
// explicitly said this machine was rebuilt. The caller audits it.
func (s *Service) applyEnrollment(ctx context.Context, t *store.EnrollToken,
	report EnrollReport, init model.InitSystem) (*model.Node, bool, error) {

	if t.TargetNodeID != nil {
		node, err := s.store.NodeByID(ctx, *t.TargetNodeID)
		if err != nil {
			return nil, false, fmt.Errorf("reinstall target: %w", err)
		}
		node.AuthType = model.AuthKey
		node.AuthSecret = t.PrivateKey
		node.HostKey = ""
		node.Arch = report.Arch
		node.OSID = report.OSID
		node.InitSystem = init
		node.RealmVersion = report.RealmVersion
		node.Status = model.StatusUnknown
		// A rebuilt machine may well answer differently, and the old verdict
		// would otherwise stand unchallenged until something re-probed.
		node.UDPCapable = nil
		if err := s.store.UpdateNode(ctx, node); err != nil {
			return nil, false, err
		}
		return node, true, nil
	}

	node := &model.Node{
		Name:         t.Name,
		Host:         t.Host,
		SSHPort:      t.SSHPort,
		SSHUser:      t.SSHUser,
		AuthType:     model.AuthKey,
		AuthSecret:   t.PrivateKey,
		ViaNodeID:    t.ViaNodeID,
		PortStart:    t.PortStart,
		PortEnd:      t.PortEnd,
		SkipUDPProbe: t.SkipUDPProbe,
		Arch:         report.Arch,
		OSID:         report.OSID,
		InitSystem:   init,
		RealmVersion: report.RealmVersion,
		Status:       model.StatusUnknown,
	}
	if err := s.store.CreateNode(ctx, node); err != nil {
		return nil, false, err
	}
	return node, false, nil
}

func orNone(s string) string {
	if s == "" {
		return "未安装"
	}
	return s
}

// RealmBinaryForToken serves the pinned realm build to an enrolling node, so
// machines with poor access to GitHub never have to fetch it themselves.
func (s *Service) RealmBinaryForToken(ctx context.Context, token, arch string) ([]byte, error) {
	t, err := s.store.EnrollTokenByValue(ctx, token)
	if err != nil {
		return nil, ErrEnrollTokenInvalid
	}
	if !t.Usable(time.Now().UTC()) {
		return nil, ErrEnrollTokenInvalid
	}
	return s.realm.Binary(ctx, arch)
}
