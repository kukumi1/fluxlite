package applier

import (
	"reflect"
	"testing"
)

func TestParseDeployedMergesSourcesAndRejectsForeignNames(t *testing.T) {
	// One relay seen through its config, its wants link and its loaded unit,
	// one left with only a unit, and lines that do not name a panel relay.
	out := "dmit-data-tw\n" +
		"dmit-data-tw\n" +
		"dmit-data-tw\n" +
		"old-route-9f2c\n" +
		"\n" +
		"*\n" +
		"Hand Made\n" +
		"../etc/passwd\n" +
		"-leading-dash\n"

	got := parseDeployed(out)
	want := []string{"dmit-data-tw", "old-route-9f2c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDeployed = %q, want %q", got, want)
	}
}

func TestParseDeployedEmpty(t *testing.T) {
	if got := parseDeployed(""); len(got) != 0 {
		t.Fatalf("parseDeployed(\"\") = %q, want none", got)
	}
}
