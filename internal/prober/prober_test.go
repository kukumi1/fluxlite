package prober

import (
	"testing"

	"github.com/kukumi1/fluxlite/internal/model"
)

func TestDetectInit(t *testing.T) {
	cases := []struct {
		token string
		want  model.InitSystem
	}{
		{"systemd", model.InitSystemd},
		{"openrc", model.InitOpenRC},
		// Fallbacks for hosts carrying neither marker, where PID 1's name is
		// all there is. Alpine's busybox init reports as plain "init".
		{"init", model.InitOpenRC},
		{"busybox", model.InitOpenRC},
		{"/usr/lib/systemd/systemd", model.InitSystemd},
		{"unknown", model.InitUnknown},
		{"", model.InitUnknown},
		// busybox ps answers `-p` with a usage message rather than a name; the
		// result must not be mistaken for a supported init system.
		{"ps: unrecognized option: p", model.InitUnknown},
	}
	for _, c := range cases {
		if got := detectInit(c.token); got != c.want {
			t.Errorf("detectInit(%q) = %q, want %q", c.token, got, c.want)
		}
	}
}

func TestNormaliseArch(t *testing.T) {
	cases := map[string]string{
		"x86_64":  "amd64",
		"amd64":   "amd64",
		"aarch64": "arm64",
		"arm64":   "arm64",
		"armv7l":  "",
		"":        "",
	}
	for in, want := range cases {
		if got := normaliseArch(in); got != want {
			t.Errorf("normaliseArch(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseRealmVersion(t *testing.T) {
	cases := map[string]string{
		"Realm 2.9.4 [brutal][batched-udp][proxy][balance][transport][multi-thread]": "2.9.4",
		"realm 2.9.4": "2.9.4",
		"":            "",
		"sh: realm: not found": "",
	}
	for in, want := range cases {
		if got := parseRealmVersion(in); got != want {
			t.Errorf("parseRealmVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
