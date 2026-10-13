package singbox

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func parametersBundle() *Bundle {
	return &Bundle{Port: 21200, Name: "Original panel name", Config: json.RawMessage(`{"inbounds":[{"type":"anytls","listen_port":21200,"users":[{"password":"test-private:+/#password"}],"tls":{"enabled":true,"certificate_path":"/server/cert.pem","key_path":"/server/key.pem"}}]}`)}
}

func parametersLink() string {
	u := url.URL{Scheme: "anytls", Host: "203.0.113.11:21200", User: url.User("test-private:+/#password"), RawQuery: "sni=developer.apple.com&insecure=1&fp=chrome", Fragment: "Different external label"}
	return u.String()
}

func TestImportAnyTLSClientParametersPreservesServerIdentity(t *testing.T) {
	b := parametersBundle()
	before := bytes.Clone(b.Config)
	p, err := b.ImportAnyTLSLink(parametersLink(), "203.0.113.11")
	if err != nil {
		t.Fatal(err)
	}
	if *p != (ClientParameters{SNI: "developer.apple.com", Insecure: true, Fingerprint: "chrome"}) {
		t.Fatal("client parameters not restored")
	}
	b.ClientParameters = p
	link, raw, err := b.ExportClient("203.0.113.11")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("sni") != "developer.apple.com" || u.Query().Get("insecure") != "1" || u.Query().Get("fp") != "chrome" || u.Fragment != b.Name || u.User.Username() != "test-private:+/#password" {
		t.Fatal("link and original client identity differ")
	}
	effective, err := b.EffectiveClientParameters("203.0.113.11")
	if err != nil || effective != *p {
		t.Fatal("client JSON options differ from link", err)
	}
	if !bytes.Equal(before, b.Config) || strings.Contains(string(raw), "certificate_path") {
		t.Fatal("server configuration modified or leaked")
	}
	encoded, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var reread Bundle
	if err := json.Unmarshal(encoded, &reread); err != nil {
		t.Fatal(err)
	}
	link2, raw2, err := reread.ExportClient("203.0.113.11")
	if err != nil || link != link2 || !bytes.Equal(raw, raw2) {
		t.Fatal("bundle serialization loses export preferences", err)
	}
}

func TestAnyTLSClientParameterImportRejectsAmbiguousOrMismatchedLink(t *testing.T) {
	b := parametersBundle()
	base := parametersLink()
	for _, link := range []string{
		strings.Replace(base, "203.0.113.11", "203.0.113.12", 1), strings.Replace(base, ":21200", ":21201", 1),
		strings.Replace(base, "test-private", "different-secret", 1), strings.Replace(base, "anytls://", "trojan://", 1),
		strings.Replace(base, "sni=developer.apple.com", "sni=developer.apple.com&sni=evil.test", 1),
		strings.Replace(base, "insecure=1", "insecure=true", 1), strings.Replace(base, "fp=chrome", "fp=invalid", 1),
		strings.Replace(base, "sni=developer.apple.com", "sni=https%3A%2F%2Fexample.test", 1),
		strings.Replace(base, "fp=chrome", "fp=chrome&alpn=h2", 1), strings.Repeat("x", 8193),
	} {
		_, err := b.ImportAnyTLSLink(link, "203.0.113.11")
		if err == nil {
			t.Fatal("invalid link accepted")
		}
		if strings.Contains(err.Error(), "test-private") || strings.Contains(err.Error(), "different-secret") || strings.Contains(err.Error(), link) {
			t.Fatal("error leaked submitted link")
		}
	}
}

func TestAnyTLSClientParametersInsecureFalseAndDefaultPort(t *testing.T) {
	b := parametersBundle()
	p, err := b.ImportAnyTLSLink(strings.Replace(parametersLink(), "insecure=1", "insecure=0", 1), "203.0.113.11")
	if err != nil || p.Insecure {
		t.Fatal("explicit verification not retained", err)
	}
	b.Config = json.RawMessage(strings.Replace(string(b.Config), "21200", "443", 1))
	b.Port = 443
	for _, host := range []string{"203.0.113.11", "203.0.113.11:443"} {
		link := url.URL{Scheme: "anytls", Host: host, Path: "/", User: url.User("test-private:+/#password"), RawQuery: "sni=developer.apple.com"}
		if _, err := b.ImportAnyTLSLink(link.String(), "203.0.113.11"); err != nil {
			t.Fatal("valid default-port trailing-slash link rejected", err)
		}
	}
}

func TestClientParametersValidationAndProtocolSafety(t *testing.T) {
	for _, name := range []string{"developer.apple.com", "localhost", "203.0.113.11", "2001:db8::1", "example.test."} {
		if err := (ClientParameters{SNI: name}).Validate(); err != nil {
			t.Fatal("valid SNI rejected", err)
		}
	}
	for _, name := range []string{"evil.test?secret=x", "example.test:443", " https://example.test", "a\n.test", "a..test", "-bad.test", strings.Repeat("a", 254)} {
		if err := (ClientParameters{SNI: name}).Validate(); err == nil {
			t.Fatal("invalid SNI accepted")
		}
	}
	b := parametersBundle()
	b.Config = json.RawMessage(strings.Replace(string(b.Config), `"anytls"`, `"hysteria2"`, 1))
	if err := b.ValidateClientParameters(ClientParameters{Fingerprint: "chrome"}); err == nil {
		t.Fatal("QUIC fingerprint accepted")
	}
	if err := b.ValidateClientParameters(ClientParameters{SNI: "example.test", Insecure: true}); err != nil {
		t.Fatal(err)
	}
	b.Config = json.RawMessage(`{"inbounds":[{"type":"vless","listen_port":21200,"tls":{"enabled":true,"reality":{"enabled":true}}}]}`)
	if err := b.ValidateClientParameters(ClientParameters{Insecure: true}); err == nil {
		t.Fatal("Reality override accepted")
	}
}
