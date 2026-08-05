package service

import (
	"strings"
	"testing"
)

// A stem shorter than minSlugBaseLen gets a digest appended rather than being
// left as a one-letter unit name.
func TestSlugBaseExtendsShortStems(t *testing.T) {
	got := slugBase("tw")
	if !strings.HasPrefix(got, "tw-") {
		t.Errorf("slugBase(\"tw\") = %q, want a tw- prefixed slug", got)
	}
	if len(got) < minSlugBaseLen || !isSafeSlug(got) {
		t.Errorf("slugBase(\"tw\") = %q, not a usable slug", got)
	}
}

func TestSlugBase(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"tw-a", "tw-a"},
		{"Hong Kong Relay", "hong-kong-relay"},
		{"HK_中转_01", "hk-01"},
		{"evoxt-hk", "evoxt-hk"},
		{"a.b.c", "a-b-c"},
		{"  trim  me  ", "trim-me"},
		{"--leading--and--trailing--", "leading-and-trailing"},
	}
	for _, c := range cases {
		if got := slugBase(c.name); got != c.want {
			t.Errorf("slugBase(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// A mostly non-ASCII name must still yield something recognisable, because it
// ends up in a systemd instance name an operator has to read on the box.
func TestSlugBaseFallsBackForNonASCIINames(t *testing.T) {
	for _, name := range []string{"香港中转", "日本→台湾", "🚀🚀🚀", "！！！", "日本→台湾 线路A"} {
		got := slugBase(name)
		if len(got) < minSlugBaseLen {
			t.Errorf("slugBase(%q) = %q, too short to identify a route", name, got)
		}
		if !isSafeSlug(got) {
			t.Errorf("slugBase(%q) = %q, which is not path/unit safe", name, got)
		}
	}
}

// Different Chinese names must not collapse onto the same slug, or two routes
// would fight over one systemd unit.
func TestSlugBaseDistinguishesNonASCIINames(t *testing.T) {
	seen := make(map[string]string)
	for _, name := range []string{"香港中转", "日本中转", "台湾中转", "新加坡中转"} {
		got := slugBase(name)
		if prev, dup := seen[got]; dup {
			t.Errorf("slugBase(%q) and slugBase(%q) both produced %q", prev, name, got)
		}
		seen[got] = name
	}
}

// The same name must always map to the same slug, so a route keeps its unit
// name across restarts and re-creations.
func TestSlugBaseIsDeterministic(t *testing.T) {
	for _, name := range []string{"香港中转", "tw-a", "🚀"} {
		if slugBase(name) != slugBase(name) {
			t.Errorf("slugBase(%q) is not deterministic", name)
		}
	}
}

func TestSlugBaseIsAlwaysSafe(t *testing.T) {
	for _, name := range []string{
		"a/b", "../../etc/passwd", "a b\tc", "名字'; rm -rf /", "%s%d", "a@b:c",
		strings.Repeat("x", 200),
	} {
		got := slugBase(name)
		if !isSafeSlug(got) {
			t.Errorf("slugBase(%q) = %q, which is not path/unit safe", name, got)
		}
		if len(got) > maxSlugLen {
			t.Errorf("slugBase(%q) = %q, exceeds %d chars", name, got, maxSlugLen)
		}
	}
}

func isSafeSlug(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return false
	}
	for _, r := range s {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if !ok {
			return false
		}
	}
	return true
}
