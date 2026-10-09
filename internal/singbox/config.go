package singbox

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/curve25519"

	"github.com/kukumi1/fluxlite/internal/model"
)

// Bundle is the encrypted-at-rest payload for one managed user. LinkTemplate
// contains the literal HOST placeholder and is only returned by an explicit
// export operation.
type Bundle struct {
	Config       json.RawMessage       `json:"config"`
	Protocol     model.SingBoxProtocol `json:"protocol"`
	Name         string                `json:"name"`
	Port         int                   `json:"port"`
	LinkTemplate string                `json:"link_template"`
}

const DefaultSNI = "developer.apple.com"

var ss2022Methods = map[string]bool{
	"2022-blake3-aes-128-gcm":       true,
	"2022-blake3-aes-256-gcm":       true,
	"2022-blake3-chacha20-poly1305": true,
}

func Generate(protocol model.SingBoxProtocol, name string, port int, configDir string, cipher string) (*Bundle, error) {
	if !protocol.Valid() {
		return nil, model.ErrSingBoxProtocol
	}
	if port < 1 || port > 65535 {
		return nil, model.ErrSingBoxPort
	}
	if protocol == model.SingBoxSS2022 && !ss2022Methods[cipher] {
		cipher = "2022-blake3-aes-256-gcm"
	}
	password := ""
	var err error
	if protocol == model.SingBoxSS2022 {
		password, err = randomBase64(32)
	} else {
		password, err = randomString(32)
	}
	if err != nil {
		return nil, err
	}
	u := uuid.NewString()
	inbound := map[string]any{
		"tag":    fmt.Sprintf("fluxlite-%s", strings.ToLower(strings.ReplaceAll(name, " ", "-"))),
		"listen": "::", "listen_port": port,
	}
	link := ""
	cert := configDir + "/server.crt"
	key := configDir + "/server.key"
	switch protocol {
	case model.SingBoxSS2022:
		inbound["type"] = "shadowsocks"
		inbound["method"] = cipher
		inbound["password"] = password
		link = fmt.Sprintf("ss://%s@HOST:%d#%s", base64.RawStdEncoding.EncodeToString([]byte(cipher+":"+password)), port, url.QueryEscape(name))
	case model.SingBoxAnyTLS:
		inbound["type"] = "anytls"
		inbound["users"] = []map[string]string{{"name": name, "password": password}}
		inbound["tls"] = tlsConfig(cert, key)
		link = fmt.Sprintf("anytls://%s@HOST:%d?sni=%s&insecure=1#%s", url.QueryEscape(password), port, url.QueryEscape(DefaultSNI), url.QueryEscape(name))
	case model.SingBoxVLESSReality:
		private, public, err := realityKeypair()
		if err != nil {
			return nil, err
		}
		shortID, err := randomHex(4)
		if err != nil {
			return nil, err
		}
		inbound["type"] = "vless"
		inbound["users"] = []map[string]string{{"name": name, "uuid": u}}
		inbound["tls"] = map[string]any{"enabled": true, "server_name": "www.apple.com", "reality": map[string]any{
			"enabled": true, "handshake": map[string]any{"server": "www.apple.com", "server_port": 443},
			"private_key": private, "short_id": []string{shortID},
		}}
		link = fmt.Sprintf("vless://%s@HOST:%d?encryption=none&security=reality&sni=www.apple.com&fp=chrome&pbk=%s&sid=%s&type=tcp#%s", u, port, public, shortID, url.QueryEscape(name))
	case model.SingBoxHysteria2:
		inbound["type"] = "hysteria2"
		inbound["users"] = []map[string]string{{"password": password}}
		inbound["tls"] = tlsConfig(cert, key)
		link = fmt.Sprintf("hysteria2://%s@HOST:%d?insecure=1#%s", url.QueryEscape(password), port, url.QueryEscape(name))
	case model.SingBoxTUIC:
		inbound["type"] = "tuic"
		inbound["users"] = []map[string]string{{"uuid": u, "password": password}}
		inbound["congestion_control"] = "bbr"
		inbound["tls"] = tlsConfig(cert, key)
		link = fmt.Sprintf("tuic://%s:%s@HOST:%d?congestion_control=bbr&insecure=1#%s", u, url.QueryEscape(password), port, url.QueryEscape(name))
	case model.SingBoxTrojan:
		inbound["type"] = "trojan"
		inbound["users"] = []map[string]string{{"name": name, "password": password}}
		inbound["tls"] = tlsConfig(cert, key)
		link = fmt.Sprintf("trojan://%s@HOST:%d?security=tls&allowInsecure=1#%s", url.QueryEscape(password), port, url.QueryEscape(name))
	case model.SingBoxVMess:
		inbound["type"] = "vmess"
		inbound["users"] = []map[string]any{{"name": name, "uuid": u, "alter_id": 0}}
		link = fmt.Sprintf("vmess://%s", base64.RawStdEncoding.EncodeToString([]byte(fmt.Sprintf(`{"v":"2","ps":"%s","add":"HOST","port":"%d","id":"%s","aid":"0","scy":"auto","net":"tcp","type":"none"}`, name, port, u))))
	case model.SingBoxVLESSTLS:
		inbound["type"] = "vless"
		inbound["users"] = []map[string]string{{"name": name, "uuid": u}}
		inbound["tls"] = tlsConfig(cert, key)
		link = fmt.Sprintf("vless://%s@HOST:%d?encryption=none&security=tls&allowInsecure=1&type=tcp#%s", u, port, url.QueryEscape(name))
	}
	config := map[string]any{
		"log":       map[string]any{"level": "warn"},
		"inbounds":  []any{inbound},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
	}
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sing-box config: %w", err)
	}
	return &Bundle{Config: raw, Protocol: protocol, Name: name, Port: port, LinkTemplate: link}, nil
}

func tlsConfig(cert, key string) map[string]any {
	return map[string]any{"enabled": true, "server_name": DefaultSNI, "certificate_path": cert, "key_path": key}
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func randomBase64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random secret: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random id: %w", err)
	}
	return fmt.Sprintf("%x", b), nil
}

func realityKeypair() (string, string, error) {
	var private [32]byte
	if _, err := rand.Read(private[:]); err != nil {
		return "", "", err
	}
	public, err := curve25519.X25519(private[:], curve25519.Basepoint)
	if err != nil {
		return "", "", fmt.Errorf("reality keypair: %w", err)
	}
	return base64.RawStdEncoding.EncodeToString(private[:]), base64.RawStdEncoding.EncodeToString(public), nil
}

func (b *Bundle) Link(host string) string {
	if b.Protocol != model.SingBoxVMess {
		return strings.ReplaceAll(b.LinkTemplate, "HOST", host)
	}
	raw := strings.TrimPrefix(b.LinkTemplate, "vmess://")
	decoded, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil {
		return b.LinkTemplate
	}
	return "vmess://" + base64.RawStdEncoding.EncodeToString([]byte(strings.ReplaceAll(string(decoded), "HOST", strings.Trim(host, "[]"))))
}
