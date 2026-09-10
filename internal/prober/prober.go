// Package prober inspects a managed node: which OS and init system it runs,
// which CPU architecture it has, whether realm is installed, and whether UDP
// survives the network path to it.
package prober

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/sshx"
)

// Facts is what a probe learned about a node.
type Facts struct {
	Arch         string
	OSID         string
	InitSystem   model.InitSystem
	RealmVersion string
	Hostname     string
}

// Probe collects basic facts over an established SSH connection.
func Probe(ctx context.Context, client *ssh.Client) (*Facts, error) {
	// The init system is decided on the node rather than from PID 1's name.
	// Alpine's ps is busybox, which has no -p or -o, so asking it about PID 1
	// yields a usage message instead of an answer — and Alpine is exactly the
	// system this needs to get right. /proc/1/comm remains as a fallback for
	// hosts with neither marker.
	const script = `
uname -m
. /etc/os-release 2>/dev/null && echo "${ID:-unknown}" || echo unknown
if [ -d /run/systemd/system ]; then echo systemd
elif command -v rc-service >/dev/null 2>&1 || [ -x /sbin/openrc-run ]; then echo openrc
else cat /proc/1/comm 2>/dev/null || echo unknown
fi
(command -v realm >/dev/null && realm --version 2>/dev/null | head -1) || echo ""
hostname 2>/dev/null || echo ""
`
	out, err := sshx.RunCheck(ctx, client, script)
	if err != nil {
		return nil, fmt.Errorf("probe node: %w", err)
	}

	lines := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	get := func(i int) string {
		if i < len(lines) {
			return strings.TrimSpace(lines[i])
		}
		return ""
	}

	facts := &Facts{
		Arch:         normaliseArch(get(0)),
		OSID:         get(1),
		InitSystem:   detectInit(get(2)),
		RealmVersion: parseRealmVersion(get(3)),
		Hostname:     get(4),
	}
	if facts.Arch == "" {
		return nil, fmt.Errorf("probe node: could not determine architecture")
	}
	if !facts.InitSystem.Valid() {
		return nil, fmt.Errorf("probe node: unsupported init system %q", get(2))
	}
	return facts, nil
}

// normaliseArch maps uname output onto Go's architecture names, which is what
// realm release artifacts are keyed by.
func normaliseArch(uname string) string {
	switch uname {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return ""
	}
}

// detectInit reads the node's verdict, or PID 1's name when the node had no
// marker to go on.
func detectInit(token string) model.InitSystem {
	switch {
	case strings.Contains(token, "systemd"):
		return model.InitSystemd
	// Alpine's PID 1 reports as plain "init": busybox driving OpenRC.
	case strings.Contains(token, "init"), strings.Contains(token, "openrc"), strings.Contains(token, "busybox"):
		return model.InitOpenRC
	default:
		return model.InitUnknown
	}
}

// parseRealmVersion extracts the version from `realm --version` output such
// as "Realm 2.9.4 [brutal][batched-udp]".
func parseRealmVersion(line string) string {
	fields := strings.Fields(line)
	if len(fields) >= 2 && strings.EqualFold(fields[0], "realm") {
		return fields[1]
	}
	return ""
}

// UDPResult reports whether UDP reaches a node from a given vantage point.
type UDPResult struct {
	// Supported is nil when the probe could not run at all. A nil result must
	// never be recorded as "no UDP": that would silently disqualify a healthy
	// node from every tcp+udp route.
	Supported *bool
	// Method names the listener implementation that was used.
	Method string
	// Detail explains the outcome, especially when Supported is nil.
	Detail string
}

// listenerBackends are tried in order. Alpine images often lack python3, and
// socat's UDP-RECV has proven unreliable on some kernels, so several options
// are attempted before giving up.
var listenerBackends = []struct {
	name  string
	check string
	start func(port int, logPath string) string
}{
	{
		name:  "python3",
		check: "command -v python3",
		start: func(port int, logPath string) string {
			return fmt.Sprintf(`python3 -c '
import socket,sys
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)
s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1)
s.bind(("0.0.0.0",%d))
s.settimeout(40)
f=open("%s","a",buffering=1)
try:
    while True:
        d,a=s.recvfrom(2048)
        f.write(d.decode("utf8","replace").strip()+"\n")
except Exception:
    pass
' >/dev/null 2>&1 &`, port, logPath)
		},
	},
	{
		name:  "socat",
		check: "command -v socat",
		start: func(port int, logPath string) string {
			return fmt.Sprintf(`socat -u UDP-RECV:%d,reuseaddr OPEN:%s,create,append >/dev/null 2>&1 &`, port, logPath)
		},
	},
	{
		name:  "nc",
		check: "command -v nc",
		start: func(port int, logPath string) string {
			return fmt.Sprintf(`nc -u -l -p %d > %s 2>/dev/null &`, port, logPath)
		},
	},
	// Last because the three above are better tools where they exist, and
	// first-class because on a minimal Debian or Ubuntu image none of them do.
	// perl-base is Essential there — the package manager itself depends on it,
	// so it cannot be absent — and Socket is one of its core modules. That
	// matters most on the machines least able to fix it: a container with a
	// full disk cannot install a listener, but it already has this one.
	{
		name:  "perl",
		check: "command -v perl",
		start: func(port int, logPath string) string {
			return fmt.Sprintf(`perl -e 'use Socket;
socket(S,PF_INET,SOCK_DGRAM,getprotobyname("udp")) or exit 1;
setsockopt(S,SOL_SOCKET,SO_REUSEADDR,1);
bind(S,sockaddr_in(%d,INADDR_ANY)) or exit 1;
open(F,">>","%s") or exit 1;
select((select(F),$|=1)[0]);
$SIG{ALRM}=sub{exit 0};
alarm 40;
while(defined(recv(S,my $d,2048,0))){$d=~s/\s+\z//;print F "$d\n";}' >/dev/null 2>&1 &`, port, logPath)
		},
	},
}

// killListenerCmd stops a listener left behind by an earlier probe.
//
// The bracket around the hyphen is load-bearing, not decoration. pkill -f
// matches against the full command line of every process, and one of those
// processes is the shell sshd spawned to run this very command — whose command
// line contains the pattern being searched for. Written plainly, the pattern
// matches the command carrying it and the shell kills itself. As a character
// class the pattern still matches "fluxlite-udp-probe" in the listener while
// its own text does not.
const killListenerCmd = `pkill -f 'fluxlite[-]udp-probe' 2>/dev/null`

// resetProbeState kills any listener left over from an earlier run and removes
// the log it wrote.
//
// The two halves are deliberately separate commands. Combined, the removal's
// own argument puts the literal log path on the command line that pkill is
// scanning, so the shell matches its own pattern and dies before reaching the
// removal — silently, because a signalled remote command surfaces as an exit
// code rather than an error. That is how the log came to survive every probe
// and answer for runs it was never part of.
func resetProbeState(ctx context.Context, target *ssh.Client, logPath string) error {
	if _, err := sshx.Run(ctx, target, killListenerCmd); err != nil {
		return err
	}
	if _, err := sshx.Run(ctx, target, fmt.Sprintf("rm -f %s", logPath)); err != nil {
		return err
	}
	return nil
}

// ProbeUDP determines whether UDP datagrams sent from source reach target at
// ingressAddr on the given port.
//
// ingressAddr must be the address relay traffic actually arrives on, which is
// the address the panel has configured for the node. It is deliberately not
// discovered on the node itself: a NAT host reaches the internet from one
// address and receives forwarded traffic on another, so self-discovery would
// test a path no relay traffic ever takes.
//
// The check is only trustworthy with a control group: the target first sends
// itself a datagram over loopback. If that fails the listener never came up
// and the external result is meaningless, so the probe reports "unknown"
// rather than a false negative.
func ProbeUDP(ctx context.Context, target, source *ssh.Client, ingressAddr string, port int) (*UDPResult, error) {
	const logPath = "/tmp/.fluxlite-udp-probe"
	const localMarker = "FLUXLITE-LOCAL"
	const remoteMarker = "FLUXLITE-REMOTE"

	backend := ""
	var startCmd string
	for _, b := range listenerBackends {
		res, err := sshx.Run(ctx, target, b.check)
		if err != nil {
			return nil, fmt.Errorf("probe udp: check %s: %w", b.name, err)
		}
		if res.ExitCode == 0 {
			backend = b.name
			startCmd = b.start(port, logPath)
			break
		}
	}
	if backend == "" {
		return &UDPResult{
			Detail: "no usable UDP listener on the node (need python3, socat, nc or perl)",
		}, nil
	}

	defer func() {
		// Best effort: a leftover listener exits on its own timeout.
		_ = resetProbeState(context.WithoutCancel(ctx), target, logPath)
	}()

	// The log must be gone before the listener starts. Every verdict below is
	// a grep over this file, so a surviving one from an earlier run answers
	// for this one.
	if err := resetProbeState(ctx, target, logPath); err != nil {
		return nil, fmt.Errorf("probe udp: cleanup: %w", err)
	}
	if _, err := sshx.Run(ctx, target, startCmd); err != nil {
		return nil, fmt.Errorf("probe udp: start listener: %w", err)
	}
	time.Sleep(2 * time.Second)

	// Control group: prove the listener is actually receiving.
	localSend := fmt.Sprintf(
		`(exec 3<>/dev/udp/127.0.0.1/%d; printf '%s\n' >&3; exec 3<&-) 2>/dev/null; sleep 1; grep -c %s %s 2>/dev/null || echo 0`,
		port, localMarker, localMarker, logPath)
	localOut, err := sshx.Run(ctx, target, localSend)
	if err != nil {
		return nil, fmt.Errorf("probe udp: control group: %w", err)
	}
	if strings.TrimSpace(localOut.Stdout) == "0" || strings.TrimSpace(localOut.Stdout) == "" {
		return &UDPResult{
			Method: backend,
			Detail: "listener did not receive the loopback control datagram; UDP support is undetermined",
		}, nil
	}

	return externalUDPCheck(ctx, target, source, ingressAddr, port, logPath, remoteMarker, backend)
}

// externalUDPCheck sends datagrams from source to the node's ingress address
// and reports whether any arrived.
func externalUDPCheck(ctx context.Context, target, source *ssh.Client, ingressAddr string, port int, logPath, marker, backend string) (*UDPResult, error) {
	if source == nil {
		return &UDPResult{
			Method: backend,
			Detail: "no vantage point available to send external UDP; support is undetermined",
		}, nil
	}

	ip := strings.TrimSpace(ingressAddr)
	if ip == "" {
		return &UDPResult{
			Method: backend,
			Detail: "node has no configured ingress address; UDP support is undetermined",
		}, nil
	}

	send := fmt.Sprintf(
		`for i in 1 2 3; do (exec 3<>/dev/udp/%s/%d; printf '%s\n' >&3; exec 3<&-) 2>/dev/null; sleep 1; done; echo sent`,
		ip, port, marker)
	if _, err := sshx.Run(ctx, source, send); err != nil {
		return nil, fmt.Errorf("probe udp: send from vantage point: %w", err)
	}
	time.Sleep(2 * time.Second)

	countOut, err := sshx.Run(ctx, target,
		fmt.Sprintf(`grep -c %s %s 2>/dev/null || echo 0`, marker, logPath))
	if err != nil {
		return nil, fmt.Errorf("probe udp: read result: %w", err)
	}

	received := strings.TrimSpace(countOut.Stdout) != "0" && strings.TrimSpace(countOut.Stdout) != ""
	return &UDPResult{
		Supported: &received,
		Method:    backend,
		Detail: fmt.Sprintf("control group passed; external datagrams to %s:%d %s",
			ip, port, map[bool]string{true: "arrived", false: "were dropped"}[received]),
	}, nil
}
