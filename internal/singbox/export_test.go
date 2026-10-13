package singbox

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/curve25519"

	"github.com/kukumi1/fluxlite/internal/model"
)

func TestExportClientGeneratedProtocols(t *testing.T) {
	for _, protocol := range []model.SingBoxProtocol{
		model.SingBoxSS2022, model.SingBoxAnyTLS, model.SingBoxVLESSReality,
		model.SingBoxHysteria2, model.SingBoxTUIC, model.SingBoxTrojan,
		model.SingBoxVMess, model.SingBoxVLESSTLS,
	} {
		t.Run(string(protocol), func(t *testing.T) {
			b, err := Generate(protocol, "香港 \"test\" #?+/", 21200, "/etc/test", "2022-blake3-aes-256-gcm")
			if err != nil {
				t.Fatal(err)
			}
			original := append([]byte(nil), b.Config...)
			for _, host := range []string{"192.0.2.2", "2001:db8::2", "[2001:db8::2]", "proxy.example.com"} {
				link, raw, err := b.ExportClient(host)
				if err != nil {
					t.Fatal(err)
				}
				out := testOutbound(t, raw)
				if link == "" || out["server"] != strings.Trim(host, "[]") || out["server_port"] != float64(21200) {
					t.Fatal("export did not use the requested server and port")
				}
				if protocol == model.SingBoxVMess {
					vmess := testVMess(t, link)
					if vmess["ps"] != b.Name || vmess["add"] != strings.Trim(host, "[]") || vmess["net"] != "tcp" {
						t.Fatal("VMess JSON escaping or host mismatch")
					}
				} else {
					uri, err := url.Parse(link)
					if err != nil || uri.Hostname() != strings.Trim(host, "[]") || uri.Port() != "21200" || uri.Fragment != b.Name {
						t.Fatal("share URI host, port, or name mismatch")
					}
				}
				assertClientOnly(t, raw)
			}
			if !bytes.Equal(original, b.Config) {
				t.Fatal("export mutated server configuration")
			}
		})
	}
}

func TestExportClientLegacyAnyTLS(t *testing.T) {
	password := "example-password :@?&#+中文/\""
	in := map[string]any{"type": "anytls", "listen": "0.0.0.0", "listen_port": 21200,
		"users": []any{map[string]any{"name": "default", "password": password}},
		"tls":   map[string]any{"enabled": true, "certificate_path": "/etc/sing-box/certs/test/cert.pem", "key_path": "/etc/sing-box/certs/test/key.pem"}}
	b := testBundle(t, in)
	b.Protocol = model.SingBoxAnyTLS
	link, raw, err := b.ExportClient("192.0.2.2")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "anytls" || u.User.Username() != password {
		t.Fatal("adopted AnyTLS credentials not preserved in link")
	}
	out := testOutbound(t, raw)
	if out["password"] != password {
		t.Fatal("adopted AnyTLS password changed")
	}
	tls := out["tls"].(map[string]any)
	if tls["insecure"] != false || u.Query().Get("insecure") != "" {
		t.Fatal("an external certificate path must not disable verification")
	}
	if _, ok := tls["server_name"]; ok || u.Query().Get("sni") != "" {
		t.Fatal("an external missing SNI must not be invented")
	}
	assertClientOnly(t, raw)
}

func TestExportClientRealityDerivesPublicKey(t *testing.T) {
	private := bytes.Repeat([]byte{42}, 32)
	public, err := curve25519.X25519(private, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, encoding := range []*base64.Encoding{base64.RawStdEncoding, base64.StdEncoding, base64.RawURLEncoding} {
		privateKey := encoding.EncodeToString(private)
		b := testBundle(t, map[string]any{"type": "vless", "listen_port": 21200,
			"users": []any{map[string]any{"uuid": "test-uuid", "flow": "xtls-rprx-vision"}},
			"tls": map[string]any{"enabled": true, "reality": map[string]any{"enabled": true,
				"private_key": privateKey, "short_id": []string{"0123abcd"}, "handshake": map[string]any{"server": "example.com"}}}})
		link, raw, err := b.ExportClient("2001:db8::2")
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(link)
		if err != nil {
			t.Fatal(err)
		}
		expected := base64.RawURLEncoding.EncodeToString(public)
		if u.Query().Get("pbk") != expected || u.Query().Get("sid") != "0123abcd" || u.Query().Get("sni") != "example.com" || u.Query().Get("flow") != "xtls-rprx-vision" {
			t.Fatal("Reality link not reconstructed from actual server keys and settings")
		}
		out := testOutbound(t, raw)
		tls := out["tls"].(map[string]any)
		if tls["reality"].(map[string]any)["public_key"] != expected || strings.Contains(string(raw), privateKey) {
			t.Fatal("Reality export leaked private key or missed public key")
		}
		assertClientOnly(t, raw)
	}
}

func TestExportClientActualVLESSTLSOverridesStaleProtocol(t *testing.T) {
	b := testBundle(t, map[string]any{"type": "vless", "listen_port": 21200,
		"users": []any{map[string]any{"uuid": "original-uuid"}},
		"tls":   map[string]any{"enabled": true, "server_name": "example.com", "alpn": []string{"h2", "http/1.1"}}})
	b.Protocol = model.SingBoxVLESSReality
	link, raw, err := b.ExportClient("192.0.2.2")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("security") != "tls" || u.Query().Get("pbk") != "" || u.Query().Get("alpn") != "h2,http/1.1" {
		t.Fatal("stale scanner protocol overrode actual TLS inbound")
	}
	if testOutbound(t, raw)["uuid"] != "original-uuid" {
		t.Fatal("UUID not preserved")
	}
}

func TestExportClientSSIdentityKeys(t *testing.T) {
	b := testBundle(t, map[string]any{"type": "shadowsocks", "listen_port": 21200,
		"method": "2022-blake3-aes-256-gcm", "password": "server+key/==", "users": []any{map[string]any{"password": "user+key/="}}})
	link, raw, err := b.ExportClient("192.0.2.2")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	password, ok := u.User.Password()
	if !ok || u.User.Username() != "2022-blake3-aes-256-gcm" || password != "server+key/==:user+key/=" {
		t.Fatal("SS2022 identity keys not preserved")
	}
	if testOutbound(t, raw)["password"] != "server+key/==:user+key/=" {
		t.Fatal("SS2022 client identity-key password mismatch")
	}
	if !strings.HasPrefix(u.User.String(), "2022-blake3-aes-256-gcm:") || !strings.Contains(u.User.String(), "%2F") || !strings.Contains(u.User.String(), "%3A") {
		t.Fatal("SS2022 identity-key userinfo must use URI percent encoding")
	}
}

func TestExportClientHysteriaObfsAndTUIC(t *testing.T) {
	b := testBundle(t, map[string]any{"type": "hysteria2", "listen_port": 21200,
		"users": []any{map[string]any{"password": "auth:@?中文"}},
		"obfs":  map[string]any{"type": "salamander", "password": "obfs?&#中文"},
		"tls":   map[string]any{"enabled": true, "server_name": "hy.example.com"}})
	link, raw, err := b.ExportClient("192.0.2.2")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil || u.User.Username() != "auth:@?中文" || u.Query().Get("obfs-password") != "obfs?&#中文" {
		t.Fatal("Hysteria2 authentication or obfuscation escaped incorrectly")
	}
	if testOutbound(t, raw)["obfs"].(map[string]any)["password"] != "obfs?&#中文" {
		t.Fatal("Hysteria2 client obfuscation not preserved")
	}
	b = testBundle(t, map[string]any{"type": "tuic", "listen_port": 21200,
		"users":              []any{map[string]any{"uuid": "original-uuid", "password": "pass:@?中文"}},
		"congestion_control": "new_reno", "tls": map[string]any{"enabled": true}})
	link, raw, err = b.ExportClient("[2001:db8::2]")
	if err != nil {
		t.Fatal(err)
	}
	u, err = url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	pass, ok := u.User.Password()
	if !ok || pass != "pass:@?中文" || u.User.Username() != "original-uuid" || testOutbound(t, raw)["congestion_control"] != "new_reno" {
		t.Fatal("TUIC credentials or congestion control not preserved")
	}
}

func TestExportClientTransports(t *testing.T) {
	for _, kind := range []string{"ws", "grpc"} {
		transport := map[string]any{"type": kind}
		if kind == "ws" {
			transport["path"], transport["headers"] = "/path?test=1&中文", map[string]string{"Host": "ws.example.com"}
		} else {
			transport["service_name"] = "service/?中文"
		}
		b := testBundle(t, map[string]any{"type": "vless", "listen_port": 21200,
			"users": []any{map[string]any{"uuid": "test-uuid"}}, "transport": transport,
			"tls": map[string]any{"enabled": true, "server_name": "example.com"}})
		link, raw, err := b.ExportClient("192.0.2.2")
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(link)
		if err != nil || u.Query().Get("type") != kind || testOutbound(t, raw)["transport"].(map[string]any)["type"] != kind {
			t.Fatal("transport not represented in share link and outbound")
		}
		if kind == "ws" && (u.Query().Get("path") != "/path?test=1&中文" || u.Query().Get("host") != "ws.example.com") {
			t.Fatal("WebSocket path or Host not preserved")
		}
		if kind == "grpc" && u.Query().Get("serviceName") != "service/?中文" {
			t.Fatal("gRPC service name not preserved")
		}
	}
}

func TestExportClientRejectsUnsafeOrAmbiguousConfig(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"type": "anytls", "listen_port": 21200,
			"users": []any{map[string]any{"password": "secret-never-in-error"}}, "tls": map[string]any{"enabled": true}}
	}
	cases := []struct {
		name string
		edit func(map[string]any)
	}{
		{"missing password", func(in map[string]any) { in["users"] = []any{map[string]any{"name": "default"}} }},
		{"no users", func(in map[string]any) { delete(in, "users") }},
		{"multiple users", func(in map[string]any) {
			in["users"] = []any{map[string]any{"password": "one"}, map[string]any{"password": "two"}}
		}},
		{"missing inbound", func(in map[string]any) { in["listen_port"] = 12345 }},
		{"missing tls", func(in map[string]any) { delete(in, "tls") }},
		{"unsupported protocol", func(in map[string]any) { in["type"] = "shadowtls" }},
		{"unknown transport", func(in map[string]any) {
			in["type"] = "vless"
			in["users"] = []any{map[string]any{"uuid": "test-uuid"}}
			in["transport"] = map[string]any{"type": "quic"}
		}},
		{"custom ws header", func(in map[string]any) {
			in["type"] = "vless"
			in["users"] = []any{map[string]any{"uuid": "test-uuid"}}
			in["transport"] = map[string]any{"type": "ws", "headers": map[string]string{"Authorization": "secret-never-in-error"}}
		}},
		{"custom transport setting", func(in map[string]any) {
			in["type"] = "vless"
			in["users"] = []any{map[string]any{"uuid": "test-uuid"}}
			in["transport"] = map[string]any{"type": "ws", "max_early_data": 512}
		}},
		{"mTLS", func(in map[string]any) {
			in["tls"] = map[string]any{"enabled": true, "client_authentication": "require-and-verify"}
		}},
		{"ECH", func(in map[string]any) {
			in["tls"] = map[string]any{"enabled": true, "ech": map[string]any{"enabled": true}}
		}},
		{"custom TLS", func(in map[string]any) {
			in["tls"] = map[string]any{"enabled": true, "cipher_suites": []string{"custom"}}
		}},
		{"invalid key", func(in map[string]any) {
			in["type"] = "vless"
			in["users"] = []any{map[string]any{"uuid": "test-uuid"}}
			in["tls"] = map[string]any{"enabled": true, "server_name": "example.com", "reality": map[string]any{"enabled": true, "private_key": "secret-never-in-error", "short_id": []string{"abcd"}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.edit(in)
			b := testBundle(t, in)
			link, raw, err := b.ExportClient("192.0.2.2")
			if !errors.Is(err, ErrClientExport) || link != "" || raw != nil || strings.Contains(err.Error(), "secret-never-in-error") {
				t.Fatal("unsafe export did not fail safely")
			}
		})
	}
	b := testBundle(t, base(), base())
	if _, _, err := b.ExportClient("192.0.2.2"); !errors.Is(err, ErrClientExport) {
		t.Fatal("duplicate matching inbounds must be rejected")
	}
	b.Config = json.RawMessage(`{"inbounds":[{"users":"secret-never-in-error"}]}`)
	if _, _, err := b.ExportClient("192.0.2.2"); !errors.Is(err, ErrClientExport) || strings.Contains(err.Error(), "secret-never-in-error") {
		t.Fatal("invalid server JSON should return sanitized error")
	}
}

func TestExportClientRejectsInvalidHost(t *testing.T) {
	b := testBundle(t, map[string]any{"type": "shadowsocks", "listen_port": 21200, "method": "2022-blake3-aes-256-gcm", "password": "secret"})
	for _, host := range []string{"", "192.0.2.2:123", "https://example.com", "example.com?test", "not an address", "example.com\n" + "@evil.test", "[invalid]", "%HOST%"} {
		if _, _, err := b.ExportClient(host); !errors.Is(err, ErrClientExport) {
			t.Fatalf("invalid host should fail safely: %q", host)
		}
	}
}

func testBundle(t *testing.T, inbounds ...map[string]any) *Bundle {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"inbounds": inbounds, "outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}}})
	if err != nil {
		t.Fatal(err)
	}
	return &Bundle{Config: raw, Port: 21200, Name: "测试节点"}
}

func testOutbound(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var client struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &client); err != nil || len(client.Outbounds) != 1 {
		t.Fatal("expected one valid client outbound")
	}
	return client.Outbounds[0]
}

func testVMess(t *testing.T, link string) map[string]any {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(link, "vmess://"))
	if err != nil {
		t.Fatal(err)
	}
	var share map[string]any
	if err := json.Unmarshal(raw, &share); err != nil {
		t.Fatal(err)
	}
	return share
}

func assertClientOnly(t *testing.T, raw json.RawMessage) {
	t.Helper()
	for _, field := range []string{`"inbounds"`, `"listen"`, `"listen_port"`, `"users"`, `"private_key"`, `"key_path"`, `"certificate_path"`} {
		if bytes.Contains(raw, []byte(field)) {
			t.Fatalf("export contains server-only field %s", field)
		}
	}
}
