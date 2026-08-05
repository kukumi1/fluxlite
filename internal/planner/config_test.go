package planner

import (
	"strings"
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
)

// Anything that reaches a node's config changes its hash, and a changed hash
// makes the applier rewrite the file and restart realm — dropping every live
// connection. A display name is free to change at any time, so it must not
// appear there.
func TestRenamingDoesNotChangeTheDeployedConfig(t *testing.T) {
	before := &model.Route{
		Name: "tw-b", Slug: "tw-b", Target: "vps.example.com:31002",
		Protocol: model.ProtocolTCP,
	}
	after := &model.Route{
		Name: "腾讯-IX-TW-SC", Slug: "tw-b", Target: "vps.example.com:31002",
		Protocol: model.ProtocolTCP,
	}

	cfgBefore := renderConfig(before, 10072, "203.0.113.9:10072")
	cfgAfter := renderConfig(after, 10072, "203.0.113.9:10072")

	if cfgBefore != cfgAfter {
		t.Errorf("renaming a route rewrote its config:\n--- before ---\n%s\n--- after ---\n%s",
			cfgBefore, cfgAfter)
	}
	if strings.Contains(cfgAfter, "腾讯") {
		t.Error("the display name reached the deployed config")
	}
	if !strings.Contains(cfgAfter, "tw-b") {
		t.Error("the config carries no identifier at all")
	}
}

// Everything that genuinely alters forwarding must still reach the node.
func TestDeployableChangesRewriteTheConfig(t *testing.T) {
	base := &model.Route{
		Name: "tw-b", Slug: "tw-b", Target: "vps.example.com:31002",
		Protocol: model.ProtocolTCP,
	}
	reference := renderConfig(base, 10072, "203.0.113.9:10072")

	udp := &model.Route{
		Name: "tw-b", Slug: "tw-b", Target: "vps.example.com:31002",
		Protocol: model.ProtocolTCPUDP,
	}
	if renderConfig(udp, 10072, "203.0.113.9:10072") == reference {
		t.Error("switching protocol left the config unchanged")
	}
	if renderConfig(base, 10099, "203.0.113.9:10072") == reference {
		t.Error("changing the listen port left the config unchanged")
	}
	if renderConfig(base, 10072, "203.0.113.9:20000") == reference {
		t.Error("changing the remote left the config unchanged")
	}
}
