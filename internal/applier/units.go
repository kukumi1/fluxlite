package applier

import (
	"fmt"
	"strings"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/planner"
)

// systemdTemplateUnit is installed once per node. Routes are started as
// instances of it, so adding a route never rewrites a shared unit file.
const systemdTemplateUnit = `[Unit]
Description=fluxlite relay (%i)
Documentation=https://github.com/kukumi1/fluxlite
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/realm -c /etc/fluxlite/realm/%i.toml
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`

const systemdUnitPath = "/etc/systemd/system/fluxlite-relay@.service"

// openrcScript is written per route. supervise-daemon is mandatory: without a
// supervisor an OOM-killed relay stays dead until someone notices, which is
// exactly how a NAT node silently stops forwarding.
func openrcScript(routeName string) string {
	cfg := planner.ConfigPath(routeName)
	return fmt.Sprintf(`#!/sbin/openrc-run
# Managed by fluxlite. Manual edits are overwritten on apply.

name="fluxlite-%s"
description="fluxlite relay for route %s"

supervisor=supervise-daemon
command="/usr/local/bin/realm"
command_args="-c %s"
pidfile="/run/fluxlite-%s.pid"
respawn_delay=3
respawn_max=0

depend() {
	need net
	after firewall
}
`, routeName, routeName, cfg, routeName)
}

func openrcScriptPath(routeName string) string {
	return "/etc/init.d/fluxlite-" + routeName
}

// serviceName is the identifier used with the node's service manager.
func serviceName(init model.InitSystem, routeName string) string {
	switch init {
	case model.InitSystemd:
		return "fluxlite-relay@" + routeName
	case model.InitOpenRC:
		return "fluxlite-" + routeName
	default:
		return ""
	}
}

// commands abstracts the service manager differences behind one interface.
type commands struct {
	reloadUnits string
	enable      string
	restart     string
	stop        string
	disable     string
	status      string
	isActive    string
	recentLog   string
}

func commandsFor(init model.InitSystem, routeName string) (commands, error) {
	svc := serviceName(init, routeName)
	if svc == "" {
		return commands{}, fmt.Errorf("unsupported init system %q", init)
	}
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

	switch init {
	case model.InitSystemd:
		return commands{
			reloadUnits: "systemctl daemon-reload",
			enable:      "systemctl enable " + q(svc),
			restart:     "systemctl restart " + q(svc),
			stop:        "systemctl stop " + q(svc) + " 2>/dev/null || true",
			disable:     "systemctl disable " + q(svc) + " 2>/dev/null || true",
			status:      "systemctl status " + q(svc) + " --no-pager 2>&1 | head -20",
			isActive:    "systemctl is-active " + q(svc),
			recentLog:   "journalctl -u " + q(svc) + " -n 10 --no-pager 2>&1 | tail -10",
		}, nil
	case model.InitOpenRC:
		return commands{
			reloadUnits: "true",
			enable:      "rc-update add " + q(svc) + " default",
			restart:     "rc-service " + q(svc) + " restart",
			stop:        "rc-service " + q(svc) + " stop 2>/dev/null || true",
			disable:     "rc-update del " + q(svc) + " default 2>/dev/null || true",
			status:      "rc-service " + q(svc) + " status 2>&1 | head -20",
			isActive:    "rc-service " + q(svc) + " status >/dev/null 2>&1 && echo active || echo inactive",
			recentLog:   "tail -10 /var/log/messages 2>/dev/null | grep -i " + q(svc) + " | tail -10",
		}, nil
	default:
		return commands{}, fmt.Errorf("unsupported init system %q", init)
	}
}
