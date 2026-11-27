package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kukumi1/fluxlite/internal/sshx"
)

// KomariInstaller is Komari's agent installer, the same one the enrollment
// script and the probe page's command run.
const KomariInstaller = "https://raw.githubusercontent.com/komari-monitor/komari-agent/refs/heads/main/install.sh"

// agentInstallTimeout bounds one install: the installer downloads the agent
// from GitHub, falling back to mirrors one after another.
const agentInstallTimeout = 3 * time.Minute

// InstallProbeAgent installs, or reinstalls, Komari's agent on a machine over
// SSH, reporting to endpoint with token. The installer replaces any agent
// already there, so this also repairs one started with the wrong settings.
func (s *Service) InstallProbeAgent(ctx context.Context, nodeID int64, endpoint, token string) error {
	node, err := s.store.NodeByID(ctx, nodeID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, agentInstallTimeout)
	defer cancel()
	client, err := s.pool.Get(ctx, node)
	if err != nil {
		return err
	}
	res, err := sshx.Run(ctx, client.Client, agentInstallScript(endpoint, token))
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("agent 安装失败（退出码 %d）：%s", res.ExitCode, lastLines(res.Stdout+res.Stderr, 4))
	}
	return nil
}

func agentInstallScript(endpoint, token string) string {
	return "set -e; f=$(mktemp); trap 'rm -f \"$f\"' EXIT; " +
		"if command -v curl >/dev/null 2>&1; then curl -fsSL --max-time 60 -o \"$f\" " + shellQuote(KomariInstaller) + "; " +
		"else wget -qO \"$f\" " + shellQuote(KomariInstaller) + "; fi; " +
		"sh \"$f\" -e " + shellQuote(endpoint) + " -t " + shellQuote(token) + " --disable-web-ssh --disable-auto-update 2>&1"
}

// lastLines keeps the end of an installer's output, where it says why it
// stopped, short enough for a message.
func lastLines(out string, n int) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " / ")
}
