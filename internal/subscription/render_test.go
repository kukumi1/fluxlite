package subscription

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/kukumi1/fluxlite/internal/model"
	"github.com/kukumi1/fluxlite/internal/singbox"
	"strings"
	"testing"
)

func fixtures(t *testing.T) []Node {
	t.Helper()
	var nodes []Node
	for _, p := range []model.SingBoxProtocol{model.SingBoxSS2022, model.SingBoxVMess, model.SingBoxVLESSTLS, model.SingBoxVLESSReality, model.SingBoxTrojan, model.SingBoxAnyTLS, model.SingBoxHysteria2, model.SingBoxTUIC} {
		b, e := singbox.Generate(p, string(p), 23456, "/tmp/fixture", "")
		if e != nil {
			t.Fatal(e)
		}
		link, config, e := b.ExportClient("192.0.2.1")
		if e != nil {
			t.Fatal(e)
		}
		nodes = append(nodes, Node{Name: string(p), Link: link, Config: config})
	}
	return nodes
}
func TestGeneratedProtocolFormats(t *testing.T) {
	nodes := fixtures(t)
	for _, format := range []string{"clash", "base64", "shadowrocket", "loon", "qx"} {
		t.Run(format, func(t *testing.T) {
			r, e := Render(format, nodes)
			if e != nil {
				t.Fatal(e)
			}
			if r.IncludedCount+len(r.Unsupported) != len(nodes) {
				t.Fatal("lost node without incompatibility report")
			}
			if strings.Contains(string(r.Content), "private_key") || strings.Contains(string(r.Content), "certificate_path") {
				t.Fatal("server config leaked")
			}
			switch format {
			case "clash":
				var v struct {
					Proxies []map[string]any `json:"proxies"`
				}
				if json.Unmarshal(r.Content, &v) != nil || len(v.Proxies) != 8 {
					t.Fatal("invalid Mihomo config")
				}
				for _, p := range v.Proxies {
					if p["server"] != "192.0.2.1" {
						t.Fatal("wrong host")
					}
				}
			case "base64", "shadowrocket":
				b, e := base64.StdEncoding.DecodeString(string(r.Content))
				if e != nil || len(strings.Fields(string(b))) != 8 {
					t.Fatal("invalid subscription list")
				}
			case "loon":
				if r.IncludedCount != 7 {
					t.Fatalf("Loon compatible count %d, reasons %v", r.IncludedCount, r.Unsupported)
				}
			case "qx":
				if r.IncludedCount != 5 {
					t.Fatalf("QX compatible count %d, reasons %v", r.IncludedCount, r.Unsupported)
				}
				if !strings.Contains(string(r.Content), "method=2022-blake3-aes-256-gcm") {
					t.Fatal("base64 credential was rejected")
				}
			}
		})
	}
}
func TestUnsupportedAndInjectionFailClosed(t *testing.T) {
	n := fixtures(t)[4]
	n.Name = "safe\nmalicious = reject"
	r, e := Render("qx", []Node{n})
	if !errors.Is(e, ErrNoCompatible) || r.IncludedCount != 0 || len(r.Content) > 0 || len(r.Unsupported) != 1 {
		t.Fatal("unsafe line name rendered")
	}
	n = fixtures(t)[3]
	r, e = Render("qx", []Node{n})
	if !errors.Is(e, ErrNoCompatible) || len(r.Unsupported) != 1 {
		t.Fatal("Reality silently downgraded")
	}
	n.Link = "trojan://secret@example.com\nextra"
	r, e = Render("base64", []Node{n})
	if !errors.Is(e, ErrNoCompatible) {
		t.Fatal("newline URI accepted")
	}
	if _, e := Render("unknown", nil); !errors.Is(e, ErrFormat) {
		t.Fatal("unknown format accepted")
	}
}
func TestUniqueNamesAndTLSPreservation(t *testing.T) {
	n := fixtures(t)[4]
	nodes := []Node{n, n, n, n}
	for i, name := range []string{"a", "a", "a (2)", "订阅节点"} {
		nodes[i].Name = name
	}
	r, e := Render("clash", nodes)
	if e != nil {
		t.Fatal(e)
	}
	var out struct {
		Proxies []map[string]any `json:"proxies"`
	}
	if e := json.Unmarshal(r.Content, &out); e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{"订阅节点": true}
	for _, p := range out.Proxies {
		name := p["name"].(string)
		if seen[name] {
			t.Fatal("duplicate/reserved name")
		}
		seen[name] = true
		if p["tls"] != true || p["skip-cert-verify"] != true {
			t.Fatal("TLS policy not preserved")
		}
	}
}
