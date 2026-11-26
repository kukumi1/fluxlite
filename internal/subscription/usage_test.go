package subscription

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUsageHeaderOmitsUnlimitedAndPermanent(t *testing.T) {
	expire := time.Unix(1798761600, 0)
	cases := []struct {
		usage Usage
		want  string
	}{
		{Usage{Upload: 1, Download: 2, Total: 3, Expire: &expire}, "upload=1; download=2; total=3; expire=1798761600"},
		{Usage{Upload: 1, Download: 2}, "upload=1; download=2"},
		{Usage{Download: 5, Expire: &expire}, "upload=0; download=5; expire=1798761600"},
	}
	for _, c := range cases {
		if got := c.usage.Header(); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}

func TestInfoNodesRenderInEveryFormat(t *testing.T) {
	for _, format := range []string{"clash", "base64", "shadowrocket", "loon", "qx"} {
		r, err := Render(format, []Node{InfoNode("⚠️ 订阅已到期，请联系管理员"), InfoNode("📊 剩余 1.00 GB / 2.00 GB")})
		if err != nil || r.IncludedCount != 2 || len(r.Unsupported) != 0 {
			t.Fatalf("%s: %+v %v", format, r, err)
		}
	}
}

func TestClashNeverStartsOnAPlaceholder(t *testing.T) {
	group := func(r Result) []any {
		var doc struct {
			Groups []struct {
				Proxies []any `json:"proxies"`
			} `json:"proxy-groups"`
		}
		if err := json.Unmarshal(r.Content, &doc); err != nil || len(doc.Groups) != 1 {
			t.Fatal(err)
		}
		return doc.Groups[0].Proxies
	}

	only, err := Render("clash", []Node{InfoNode("⚠️ 流量已用完")})
	if err != nil {
		t.Fatal(err)
	}
	if g := group(only); len(g) != 2 || g[0] != "DIRECT" {
		t.Fatalf("dead subscription group %v", g)
	}

	real := InfoNode("real")
	real.Info = false
	mixed, err := Render("clash", []Node{InfoNode("⏳ 到期 2026-12-31"), real})
	if err != nil {
		t.Fatal(err)
	}
	if g := group(mixed); len(g) != 2 || g[0] != "real" {
		t.Fatalf("placeholder first in %v", g)
	}
}

func TestUnsupportedRealNodesStillFailWithPlaceholders(t *testing.T) {
	broken := Node{Name: "broken", Link: "ss://x", Config: []byte(`{"outbounds":[]}`)}
	if _, err := Render("clash", []Node{InfoNode("⏳ 长期有效"), broken}); err != ErrNoCompatible {
		t.Fatalf("placeholders hid an incompatible subscription: %v", err)
	}
}

func TestFormatForUserAgent(t *testing.T) {
	for ua, want := range map[string]string{
		"clash-verge/v2.2.3": "clash", "mihomo/1.19": "clash", "Stash/3.0": "clash",
		"Shadowrocket/2070": "shadowrocket", "Loon/3.2": "loon", "Quantumult%20X/1.4": "qx",
		"v2rayN/7.0": "base64", "": "base64",
	} {
		if got := FormatForUserAgent(ua); got != want {
			t.Errorf("%q: got %s, want %s", ua, got, want)
		}
	}
}
