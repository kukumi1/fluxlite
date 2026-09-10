package prober

import (
	"regexp"
	"strings"
	"testing"
)

// listenerCmdline is the text every listener carries on its command line: the
// log path it was told to write to. It is what the kill pattern hunts for.
const listenerCmdline = "/tmp/.fluxlite-udp-probe"

// killPattern pulls the regex out of the kill command, so the tests below check
// the pattern that actually ships rather than a copy of it.
func killPattern(t *testing.T) *regexp.Regexp {
	t.Helper()

	parts := strings.Split(killListenerCmd, "'")
	if len(parts) < 2 {
		t.Fatalf("kill command has no quoted pattern to test: %s", killListenerCmd)
	}
	re, err := regexp.Compile(parts[1])
	if err != nil {
		t.Fatalf("kill pattern is not a valid regex: %v", err)
	}
	return re
}

// This is the regression guard for the bug that made every UDP verdict
// untrustworthy. pkill -f matches the full command line of every process,
// including the shell running the pkill. When the kill and the log removal
// shared one command line, the pattern matched that shell, the shell died
// before reaching the removal, and — because a signalled remote command comes
// back as an exit code rather than an error — nothing reported a problem. The
// log then survived from run to run, and since every verdict is a grep over
// it, a node that once passed passed forever.
func TestKillPatternDoesNotMatchItsOwnCommand(t *testing.T) {
	re := killPattern(t)

	if re.MatchString(killListenerCmd) {
		t.Fatalf("kill command matches its own pattern, so it kills the shell "+
			"running it: %s", killListenerCmd)
	}
	if strings.Contains(killListenerCmd, listenerCmdline) {
		t.Fatalf("kill command carries the listener's command line text, so any "+
			"command sharing a line with it becomes a target: %s", killListenerCmd)
	}
}

// The pattern is only safe if it is still effective: a listener that survives
// cleanup keeps the port bound and the next probe's listener cannot start.
func TestKillPatternMatchesEveryListener(t *testing.T) {
	re := killPattern(t)

	for _, b := range listenerBackends {
		t.Run(b.name, func(t *testing.T) {
			cmd := b.start(44736, listenerCmdline)
			if !re.MatchString(cmd) {
				t.Fatalf("cleanup cannot kill the %s listener; it would keep the "+
					"port bound and block the next probe:\n%s", b.name, cmd)
			}
		})
	}
}

// Minimal Debian and Ubuntu images ship none of python3, socat or nc, so the
// probe used to give up on them and report "unknown" forever. perl-base is
// Essential on those systems and cannot be missing.
func TestPerlListenerIsAvailableAsFallback(t *testing.T) {
	var names []string
	for _, b := range listenerBackends {
		names = append(names, b.name)
	}

	if names[len(names)-1] != "perl" {
		t.Errorf("perl should be tried last, after the purpose-built tools; order is %v", names)
	}

	var perl *struct {
		name  string
		check string
		start func(port int, logPath string) string
	}
	for i := range listenerBackends {
		if listenerBackends[i].name == "perl" {
			perl = &listenerBackends[i]
		}
	}
	if perl == nil {
		t.Fatal("no perl backend, so minimal Debian nodes still cannot report UDP")
	}

	cmd := perl.start(44736, listenerCmdline)
	for _, want := range []string{
		"sockaddr_in(44736,INADDR_ANY)", // binds the port it was asked to
		listenerCmdline,                 // writes where the greps will look
		"alarm 40",                      // gives up rather than lingering
		"&",                             // backgrounded, or the probe blocks
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("perl listener is missing %q:\n%s", want, cmd)
		}
	}

	// The whole script sits inside a single-quoted shell argument, so a single
	// quote anywhere in it would end the argument and hand the rest to the
	// shell as commands.
	if inner := strings.TrimSuffix(strings.TrimPrefix(cmd, "perl -e '"), "' >/dev/null 2>&1 &"); strings.Contains(inner, "'") {
		t.Error("perl script contains a single quote, which would terminate the shell argument early")
	}
}
