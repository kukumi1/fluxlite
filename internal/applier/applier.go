// Package applier reconciles a node's on-disk state with the configuration a
// route plan calls for.
//
// Every write is guarded by a hash comparison. realm has no config reload, so
// applying an unchanged configuration would restart the relay and drop live
// connections for nothing.
package applier

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/planner"
	"github.com/kukumi1/fluxlite/internal/sshx"
)

const realmPath = "/usr/local/bin/realm"

// Applier deploys route plans onto nodes.
type Applier struct {
	pool  *sshx.Pool
	realm RealmSource
}

func New(pool *sshx.Pool, realm RealmSource) *Applier {
	return &Applier{pool: pool, realm: realm}
}

// HopOutcome describes what happened on one hop.
type HopOutcome struct {
	NodeName string `json:"node_name"`
	HopOrder int    `json:"hop_order"`
	Listen   int    `json:"listen"`
	Remote   string `json:"remote"`
	Changed  bool   `json:"changed"`
	Action   string `json:"action"`
	Error    string `json:"error,omitempty"`
}

// Result aggregates the outcome of applying a route.
type Result struct {
	RouteName string       `json:"route_name"`
	Hops      []HopOutcome `json:"hops"`
}

// Failed reports whether any hop errored.
func (r *Result) Failed() bool {
	for _, h := range r.Hops {
		if h.Error != "" {
			return true
		}
	}
	return false
}

// Apply deploys every hop of a plan. Hops are configured from the last one
// backwards so that a relay never points at a listener that does not exist
// yet; the entry hop goes live only once the whole chain behind it is up.
func (a *Applier) Apply(ctx context.Context, plan *planner.Plan) (*Result, error) {
	result := &Result{RouteName: plan.Route.Name}
	outcomes := make([]HopOutcome, len(plan.Hops))

	for i := len(plan.Hops) - 1; i >= 0; i-- {
		hop := plan.Hops[i]
		outcome := HopOutcome{
			NodeName: hop.Node.Name,
			HopOrder: hop.HopOrder,
			Listen:   hop.Listen,
			Remote:   hop.Remote,
		}

		changed, action, err := a.applyHop(ctx, &hop)
		outcome.Changed = changed
		outcome.Action = action
		if err != nil {
			outcome.Error = err.Error()
		}
		outcomes[i] = outcome

		// A broken hop makes every hop in front of it pointless, and bringing
		// the entry online would only advertise a dead path.
		if err != nil {
			for j := i - 1; j >= 0; j-- {
				outcomes[j] = HopOutcome{
					NodeName: plan.Hops[j].Node.Name,
					HopOrder: plan.Hops[j].HopOrder,
					Listen:   plan.Hops[j].Listen,
					Remote:   plan.Hops[j].Remote,
					Action:   "skipped",
					Error:    "upstream hop failed",
				}
			}
			break
		}
	}

	result.Hops = outcomes
	return result, nil
}

func (a *Applier) applyHop(ctx context.Context, hop *planner.HopPlan) (bool, string, error) {
	client, err := a.pool.Get(ctx, hop.Node)
	if err != nil {
		return false, "unreachable", fmt.Errorf("connect: %w", err)
	}

	if err := a.ensureRealm(ctx, client.Client, hop.Node); err != nil {
		return false, "realm-install-failed", err
	}
	if err := a.ensureUnit(ctx, client.Client, hop.Node, hop.RouteSlug); err != nil {
		return false, "unit-install-failed", err
	}

	current, err := a.currentConfig(ctx, client.Client, hop.ConfigPath)
	if err != nil {
		return false, "read-config-failed", err
	}
	if current == hop.Config {
		// Still confirm the service is actually running: a matching config on
		// a dead relay is the failure mode that hides longest.
		active, err := a.isActive(ctx, client.Client, hop.Node, hop.RouteSlug)
		if err != nil {
			return false, "status-check-failed", err
		}
		if active {
			return false, "unchanged", nil
		}
		if err := a.restart(ctx, client.Client, hop.Node, hop.RouteSlug); err != nil {
			return false, "restart-failed", err
		}
		return true, "restarted-dead-service", nil
	}

	if err := sshx.WriteFile(ctx, client.Client, hop.ConfigPath, []byte(hop.Config), "0600"); err != nil {
		return false, "write-config-failed", err
	}
	if err := a.restart(ctx, client.Client, hop.Node, hop.RouteSlug); err != nil {
		return false, "restart-failed", err
	}
	return true, "applied", nil
}

// ensureRealm installs or upgrades the pinned realm build on the node and
// makes sure the directories it needs exist.
func (a *Applier) ensureRealm(ctx context.Context, client *ssh.Client, node *model.Node) error {
	// These must be created unconditionally. realm refuses to start when it
	// cannot open its log file, and a node that already carries the right
	// realm build would otherwise skip directory creation entirely and end up
	// in a restart loop.
	if _, err := sshx.RunCheck(ctx, client, "mkdir -p /etc/fluxlite/realm /var/log/fluxlite"); err != nil {
		return fmt.Errorf("create directories: %w", err)
	}

	res, err := sshx.Run(ctx, client, realmPath+" --version 2>/dev/null | head -1")
	if err != nil {
		return fmt.Errorf("check realm: %w", err)
	}
	installed := ""
	if fields := strings.Fields(strings.TrimSpace(res.Stdout)); len(fields) >= 2 {
		installed = fields[1]
	}
	if installed == a.realm.Version() {
		return nil
	}

	bin, err := a.realm.Binary(ctx, node.Arch)
	if err != nil {
		return fmt.Errorf("obtain realm for %s: %w", node.Arch, err)
	}
	if err := sshx.WriteFile(ctx, client, realmPath, bin, "0755"); err != nil {
		return fmt.Errorf("upload realm: %w", err)
	}
	if _, err := sshx.RunCheck(ctx, client, realmPath+" --version"); err != nil {
		return fmt.Errorf("verify realm after upload: %w", err)
	}
	return nil
}

// ensureUnit installs the service definition for the node's init system.
func (a *Applier) ensureUnit(ctx context.Context, client *ssh.Client, node *model.Node, slug string) error {
	cmds, err := commandsFor(node.InitSystem, slug)
	if err != nil {
		return err
	}

	switch node.InitSystem {
	case model.InitSystemd:
		current, err := a.currentConfig(ctx, client, systemdUnitPath)
		if err != nil {
			return err
		}
		if current != systemdTemplateUnit {
			if err := sshx.WriteFile(ctx, client, systemdUnitPath, []byte(systemdTemplateUnit), "0644"); err != nil {
				return fmt.Errorf("write systemd unit: %w", err)
			}
			if _, err := sshx.RunCheck(ctx, client, cmds.reloadUnits); err != nil {
				return fmt.Errorf("daemon-reload: %w", err)
			}
		}
	case model.InitOpenRC:
		script := openrcScript(slug)
		path := openrcScriptPath(slug)
		current, err := a.currentConfig(ctx, client, path)
		if err != nil {
			return err
		}
		if current != script {
			if err := sshx.WriteFile(ctx, client, path, []byte(script), "0755"); err != nil {
				return fmt.Errorf("write openrc script: %w", err)
			}
		}
	default:
		return fmt.Errorf("unsupported init system %q on node %s", node.InitSystem, node.Name)
	}

	if _, err := sshx.Run(ctx, client, cmds.enable); err != nil {
		return fmt.Errorf("enable service: %w", err)
	}
	return nil
}

// currentConfig reads a remote file, treating absence as empty content.
func (a *Applier) currentConfig(ctx context.Context, client *ssh.Client, path string) (string, error) {
	res, err := sshx.Run(ctx, client, "cat "+sshx.Quote(path)+" 2>/dev/null")
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return res.Stdout, nil
}

func (a *Applier) restart(ctx context.Context, client *ssh.Client, node *model.Node, slug string) error {
	cmds, err := commandsFor(node.InitSystem, slug)
	if err != nil {
		return err
	}
	if _, err := sshx.RunCheck(ctx, client, cmds.restart); err != nil {
		return fmt.Errorf("restart service: %w (%s)", err, a.serviceDetail(ctx, client, cmds))
	}

	// A restart command succeeding proves nothing: with Restart=always a relay
	// that dies on startup leaves systemd reporting success while the service
	// sits in a crash loop. Confirm it is still alive a moment later.
	if err := sleepCtx(ctx, 2*time.Second); err != nil {
		return err
	}
	active, err := a.isActive(ctx, client, node, slug)
	if err != nil {
		return err
	}
	if !active {
		return fmt.Errorf("service did not stay running after restart: %s",
			a.serviceDetail(ctx, client, cmds))
	}
	return nil
}

// serviceDetail collects whatever the service manager and realm's own log can
// say about a failure, so the operator is not left with a bare exit code.
func (a *Applier) serviceDetail(ctx context.Context, client *ssh.Client, cmds commands) string {
	var parts []string
	if status, err := sshx.Run(ctx, client, cmds.status); err == nil {
		if s := strings.TrimSpace(status.Stdout); s != "" {
			parts = append(parts, s)
		}
	}
	if logs, err := sshx.Run(ctx, client, cmds.recentLog); err == nil {
		if s := strings.TrimSpace(logs.Stdout); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " | ")
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (a *Applier) isActive(ctx context.Context, client *ssh.Client, node *model.Node, slug string) (bool, error) {
	cmds, err := commandsFor(node.InitSystem, slug)
	if err != nil {
		return false, err
	}
	res, err := sshx.Run(ctx, client, cmds.isActive)
	if err != nil {
		return false, fmt.Errorf("check service state: %w", err)
	}
	return strings.TrimSpace(res.Stdout) == "active", nil
}

// Remove stops and deletes a route's deployment from a node.
func (a *Applier) Remove(ctx context.Context, node *model.Node, slug string) error {
	client, err := a.pool.Get(ctx, node)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", node.Name, err)
	}
	cmds, err := commandsFor(node.InitSystem, slug)
	if err != nil {
		return err
	}

	if _, err := sshx.Run(ctx, client.Client, cmds.stop); err != nil {
		return fmt.Errorf("stop service: %w", err)
	}
	if _, err := sshx.Run(ctx, client.Client, cmds.disable); err != nil {
		return fmt.Errorf("disable service: %w", err)
	}

	paths := []string{planner.ConfigPath(slug)}
	if node.InitSystem == model.InitOpenRC {
		paths = append(paths, openrcScriptPath(slug))
	}
	for _, p := range paths {
		if _, err := sshx.Run(ctx, client.Client, "rm -f "+sshx.Quote(p)); err != nil {
			return fmt.Errorf("remove %s: %w", p, err)
		}
	}
	if node.InitSystem == model.InitSystemd {
		if _, err := sshx.Run(ctx, client.Client, cmds.reloadUnits); err != nil {
			return fmt.Errorf("daemon-reload: %w", err)
		}
	}
	return nil
}

// Status reports whether a route's relay is running on a node.
func (a *Applier) Status(ctx context.Context, node *model.Node, slug string) (bool, error) {
	client, err := a.pool.Get(ctx, node)
	if err != nil {
		return false, fmt.Errorf("connect to %s: %w", node.Name, err)
	}
	return a.isActive(ctx, client.Client, node, slug)
}
