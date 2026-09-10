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
