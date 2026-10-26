package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/singbox"
	"github.com/kukumi1/fluxlite/internal/sshx"
)

// SingBoxPortMove reports a finished port change.
type SingBoxPortMove struct {
	Port int `json:"port"`
	// LandingRoutes names routes whose landing address is this node's old
	// port. They are not rewritten: changing where a route lands is the
	// operator's call, and they need to know it is now pointing at nothing.
	LandingRoutes []string `json:"landing_routes"`
}

// ChangeSingBoxPort moves a managed node to another port on the same machine.
//
// The new config is checked by sing-box before it replaces the old one, and a
// running service that fails to come back up on the new port is put back on
// the old config, so a bad move leaves the node as it was. Credentials do not
// change: existing clients only need the new port.
func (s *Service) ChangeSingBoxPort(ctx context.Context, id int64, port int) (*SingBoxPortMove, error) {
	if err := s.subscriptionIndependentOnly(ctx, id); err != nil {
		return nil, err
	}
	release, err := s.lockSingBoxAdoption(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	u, err := s.store.SingBoxUserByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if u.Source != model.SingBoxManaged {
		return nil, ErrSingBoxNotManaged
	}
	if port == u.Port {
		return &SingBoxPortMove{Port: port}, nil
	}
	node, err := s.store.NodeByID(ctx, u.NodeID)
	if err != nil {
		return nil, err
	}

	used, err := s.newLiveLookup(nil).UsedPortsOnNode(ctx, node.ID, nil)
	if err != nil {
		return nil, err
	}
	if _, err := selectSingBoxPort(ctx, node, port, used, func(ctx context.Context, p int) (bool, error) {
		return s.store.PortClaimed(ctx, node.ID, p, u.ID)
	}); err != nil {
		return nil, err
	}

	plain, err := s.sealer.Open(u.ConfigBlob)
	if err != nil {
		return nil, err
	}
	var bundle singbox.Bundle
	if err := json.Unmarshal(plain, &bundle); err != nil {
		return nil, err
	}
	bundle.Port = u.Port
	if err := bundle.WithPort(port); err != nil {
		return nil, err
	}
	sealed, err := s.sealer.Seal(mustJSON(bundle))
	if err != nil {
		return nil, err
	}

	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", node.Name, err)
	}
	if err := s.swapSingBoxConfig(ctx, client.Client, node, u, bundle.Config, port); err != nil {
		return nil, err
	}

	oldPort := u.Port
	if err := s.store.MoveSingBoxPort(ctx, u.ID, oldPort, port, u.ConfigBlob, sealed); err != nil {
		return nil, err
	}
	u.Port = port

	// Counting and rate limiting are keyed on the port. Neither is allowed to
	// fail the move: the node already serves on the new port, and both are
	// re-asserted by the traffic collector and the next edit.
	if err := s.moveSingBoxAccounting(ctx, client.Client, u); err != nil {
		s.log.Warn("sing-box accounting not moved to the new port", "user", u.Name, "error", err)
	}
	if err := applySingBoxRateLimit(ctx, client.Client, u); err != nil {
		s.log.Warn("sing-box rate limit not moved to the new port", "user", u.Name, "error", err)
	}

	landing, err := s.routesLandingOn(ctx, node, oldPort)
	if err != nil {
		return nil, err
	}
	return &SingBoxPortMove{Port: port, LandingRoutes: landing}, nil
}

// swapSingBoxConfig replaces the node's config with one that sing-box has
// already accepted, restarting the service only if it was running.
func (s *Service) swapSingBoxConfig(ctx context.Context, client *ssh.Client, node *model.Node, u *model.SingBoxUser, server json.RawMessage, port int) error {
	config, _, err := singbox.WithAuditLog(server, u.ID)
	if err != nil {
		return err
	}
	dir := fmt.Sprintf("/etc/fluxlite/singbox/%d", u.ID)
	data := fmt.Sprintf("/var/lib/fluxlite-singbox/%d", u.ID)
	owner := singBoxLinuxUser(u.ID)
	if err := sshx.WriteFile(ctx, client, dir+"/config.json.new", config, "0600"); err != nil {
		return err
	}

	active, restart, err := singBoxServiceProbes(node.InitSystem, u)
	if err != nil {
		return err
	}
	cmd := swapConfigScript(dir, data, owner, active, restart)

	res, err := sshx.Run(ctx, client, cmd)
	if err != nil {
		return err
	}
	switch res.ExitCode {
	case 0:
		return nil
	case 10:
		return fmt.Errorf("sing-box rejected the new config: %s", strings.TrimSpace(res.Stderr+res.Stdout))
	case 11:
		return fmt.Errorf("sing-box did not stay up on port %d; the old config was restored", port)
	case 12:
		return fmt.Errorf("sing-box is not installed on %s", node.Name)
	default:
		return fmt.Errorf("swap sing-box config: exit %d: %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
}

// singBoxServiceProbes returns a command that succeeds while the service is
// running and one that restarts it and confirms it stayed up.
func singBoxServiceProbes(init model.InitSystem, u *model.SingBoxUser) (active, restart string, err error) {
	if u.ServiceName != fmt.Sprintf("fluxlite-singbox-%d", u.ID) {
		return "", "", fmt.Errorf("refusing unit operation outside managed sing-box namespace")
	}
	switch init {
	case model.InitSystemd:
		unit := shellQuote(u.ServiceName + ".service")
		return "systemctl is-active --quiet " + unit,
			"systemctl restart " + unit + " && sleep 2 && systemctl is-active --quiet " + unit, nil
	case model.InitOpenRC:
		name := shellQuote(u.ServiceName)
		return "rc-service " + name + " status >/dev/null 2>&1",
			"rc-service " + name + " restart && sleep 2 && rc-service " + name + " status >/dev/null 2>&1", nil
	default:
		return "", "", fmt.Errorf("unsupported init system %q", init)
	}
}

// moveSingBoxAccounting drops a user's counting rules and installs them for
// its current port. The rebuilt counters start from zero, which the traffic
// collector already treats as a reset rather than as negative usage.
func (s *Service) moveSingBoxAccounting(ctx context.Context, client *ssh.Client, u *model.SingBoxUser) error {
	// The trailing colon keeps user 27 from matching user 270.
	tag := fmt.Sprintf("fluxlite-sb:%d:", u.ID)
	drop := `for bin in iptables ip6tables; do
    command -v "$bin" >/dev/null 2>&1 || continue
    "$bin" -S FLUXLITE_SB_ACCT 2>/dev/null | grep -F -- ` + shellQuote(tag) + ` | sed 's/^-A /-D /' | while read -r rule; do eval "\"$bin\" $rule"; done
done`
	if _, err := sshx.RunCheck(ctx, client, drop); err != nil {
		return fmt.Errorf("drop old counting rules: %w", err)
	}
	return s.ensureSingBoxAccounting(ctx, client, u.ID, u.Port)
}

// routesLandingOn lists routes whose landing address is the node at port.
// Only addresses the panel knows for the node are compared; a route landing
// through a domain name pointing at it cannot be recognised.
func (s *Service) routesLandingOn(ctx context.Context, node *model.Node, port int) ([]string, error) {
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return nil, err
	}
	hosts := map[string]bool{}
	for _, h := range []string{node.Host, node.IPv6Address, node.IPv6Entry} {
		if h = strings.Trim(h, "[]"); h != "" {
			hosts[strings.ToLower(h)] = true
		}
	}
	var names []string
	for _, r := range routes {
		host, p, err := net.SplitHostPort(r.Target)
		if err != nil || p != strconv.Itoa(port) {
			continue
		}
		if hosts[strings.ToLower(host)] {
			names = append(names, r.Name)
		}
	}
	return names, nil
}

// swapConfigScript installs dir/config.json.new as dir/config.json once
// sing-box accepts it, restarting the service only if it was running: a node
// disabled or out of quota must not come back to life because its port moved.
//
// Exit 10: the new config was rejected and nothing changed. Exit 11: the
// service did not come back up, so the previous config was put back and the
// service restarted on it. Exit 12: no sing-box binary.
func swapConfigScript(dir, data, owner, active, restart string) string {
	return fmt.Sprintf(`set -e
dir=%s
bin=$(command -v sing-box || command -v /usr/local/bin/sing-box) || exit 12
chown %s:%s "$dir/config.json.new"
if ! "$bin" check -D %s -c "$dir/config.json.new"; then rm -f "$dir/config.json.new"; exit 10; fi
running=0
if %s; then running=1; fi
cp -p "$dir/config.json" "$dir/config.json.prev"
mv -f "$dir/config.json.new" "$dir/config.json"
if [ "$running" = 1 ] && ! { %s; }; then
    mv -f "$dir/config.json.prev" "$dir/config.json"
    { %s; } || true
    exit 11
fi
rm -f "$dir/config.json.prev"`,
		shellQuote(dir), shellQuote(owner), shellQuote(owner), shellQuote(data), active, restart, restart)
}
