package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The install command passes the agent's address and token as single words,
// whatever they hold, and is valid sh.
func TestAgentInstallScript(t *testing.T) {
	script := agentInstallScript("https://status.example.com", "to'ken")
	if !strings.Contains(script, `-e 'https://status.example.com' -t 'to'\''ken'`) {
		t.Fatal(script)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sh, "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
