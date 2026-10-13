package singbox

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/crypto/curve25519"
)

// ErrClientExport identifies safe, user-facing export validation failures.
// Errors never include server configuration, credentials, or private keys.
var ErrClientExport = errors.New("无法生成客户端配置")

type exportInbound struct {
	Type              string          `json:"type"`
	Port              int             `json:"listen_port"`
	Method            string          `json:"method"`
	Password          string          `json:"password"`
	Users             []exportUser    `json:"users"`
	TLS               *exportTLS      `json:"tls"`
	Transport         json.RawMessage `json:"transport"`
	Obfs              *exportObfs     `json:"obfs"`
	Destinations      json.RawMessage `json:"destinations"`
	CongestionControl string          `json:"congestion_control"`
}

type exportUser struct {
	Password string `json:"password"`
	UUID     string `json:"uuid"`
	Flow     string `json:"flow"`
	AlterID  int    `json:"alter_id"`
}

type exportObfs struct {
	Type     string `json:"type"`
	Password string `json:"password"`
}

type exportTLS struct {
	Enabled              bool            `json:"enabled"`
	ServerName           string          `json:"server_name"`
	ALPN                 json.RawMessage `json:"alpn"`
	MinVersion           string          `json:"min_version"`
	MaxVersion           string          `json:"max_version"`
	CipherSuites         json.RawMessage `json:"cipher_suites"`
	CurvePreferences     json.RawMessage `json:"curve_preferences"`
	ClientAuthentication string          `json:"client_authentication"`
	ECH                  *struct {
		Enabled bool `json:"enabled"`
	} `json:"ech"`
	Reality *struct {
		Enabled    bool            `json:"enabled"`
		PrivateKey string          `json:"private_key"`
		ShortID    json.RawMessage `json:"short_id"`
		Handshake  struct {
			Server string `json:"server"`
		} `json:"handshake"`
	} `json:"reality"`
}

// ExportClient reconstructs an outbound and a share URI from the server's
// actual inbound, including legacy adopted bundles without LinkTemplate.
// It is read-only: server configuration and original credentials are preserved.
func (b *Bundle) ExportClient(host string) (string, json.RawMessage, error) {
	if b == nil || b.Port < 1 || b.Port > 65535 {
		return "", nil, exportError("节点端口无效")
	}
	host, err := exportHost(host)
	if err != nil {
		return "", nil, err
	}
	var server struct {
		Inbounds []exportInbound `json:"inbounds"`
	}
	if err := json.Unmarshal(b.Config, &server); err != nil {
		return "", nil, exportError("原节点配置格式无效")
	}
	var inbound *exportInbound
	for i := range server.Inbounds {
		if server.Inbounds[i].Port == b.Port {
			if inbound != nil {
				return "", nil, exportError("同一端口存在多个入站配置，请先确认要导出的节点")
			}
			inbound = &server.Inbounds[i]
		}
	}
	if inbound == nil {
		return "", nil, exportError("原配置中未找到与节点端口一致的入站")
	}
	out := map[string]any{"type": inbound.Type, "tag": "proxy", "server": host, "server_port": b.Port}
	query := make(url.Values)
	var user exportUser
	if inbound.Type != "shadowsocks" {
		if len(inbound.Users) != 1 {
			return "", nil, exportError("入站必须包含唯一用户；多用户节点需先拆分后导出")
		}
		user = inbound.Users[0]
	}
	scheme, credential := inbound.Type, ""
	switch inbound.Type {
	case "shadowsocks":
		if !ss2022Methods[inbound.Method] {
			return "", nil, exportError("该 Shadowsocks 加密方式暂不支持导出")
		}
		if nonemptyJSON(inbound.Destinations) {
			return "", nil, exportError("Shadowsocks 中继目标配置暂不支持导出")
		}
		password := inbound.Password
		if len(inbound.Users) > 1 {
			return "", nil, exportError("入站包含多个用户，请先拆分后导出")
		}
		if len(inbound.Users) == 1 {
			if password == "" || inbound.Users[0].Password == "" {
				return "", nil, exportError("Shadowsocks 缺少服务端密钥或用户密钥")
			}
			if inbound.Method == "2022-blake3-chacha20-poly1305" {
				return "", nil, exportError("该 Shadowsocks 多用户加密方式暂不支持导出")
			}
			password += ":" + inbound.Users[0].Password
		}
		if password == "" {
			return "", nil, exportError("节点缺少连接密码")
		}
		out["method"], out["password"] = inbound.Method, password
		scheme = "ss"
		credential = inbound.Method
	case "anytls", "hysteria2", "trojan":
		if user.Password == "" {
			return "", nil, exportError("节点缺少连接密码")
		}
		out["password"], credential = user.Password, user.Password
	case "tuic":
		if user.UUID == "" || user.Password == "" {
			return "", nil, exportError("TUIC 缺少用户标识或连接密码")
		}
		out["uuid"], out["password"] = user.UUID, user.Password
		credential = user.UUID
		if cc := inbound.CongestionControl; cc != "" {
			if cc != "bbr" && cc != "cubic" && cc != "new_reno" {
				return "", nil, exportError("TUIC 拥塞控制方式暂不支持导出")
			}
			out["congestion_control"] = cc
			query.Set("congestion_control", cc)
		}
	case "vless", "vmess":
		if user.UUID == "" {
			return "", nil, exportError("节点缺少用户标识")
		}
		out["uuid"], credential = user.UUID, user.UUID
		if inbound.Type == "vless" {
			query.Set("encryption", "none")
			if user.Flow != "" {
				if user.Flow != "xtls-rprx-vision" {
					return "", nil, exportError("VLESS 流控方式暂不支持导出")
				}
				out["flow"] = user.Flow
				query.Set("flow", user.Flow)
			}
		} else {
			if user.AlterID < 0 {
				return "", nil, exportError("VMess 用户配置无效")
			}
			out["security"], out["alter_id"] = "auto", user.AlterID
		}
	default:
		return "", nil, exportError("该节点协议暂不支持客户端导出")
	}
	if err := b.exportTLS(inbound, out, query); err != nil {
		return "", nil, err
	}
	if err := exportTransport(inbound, out, query); err != nil {
		return "", nil, err
	}
	if inbound.Obfs != nil && inbound.Obfs.Type != "" {
		if inbound.Type != "hysteria2" || inbound.Obfs.Type != "salamander" || inbound.Obfs.Password == "" {
			return "", nil, exportError("该混淆配置暂不支持客户端导出")
		}
		out["obfs"] = map[string]any{"type": "salamander", "password": inbound.Obfs.Password}
		query.Set("obfs", "salamander")
		query.Set("obfs-password", inbound.Obfs.Password)
	}
	link := ""
	if scheme == "vmess" {
		link, err = vmessShare(out, user, query, b.Name)
	} else {
		share := url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.Itoa(b.Port)), User: url.User(credential), RawQuery: query.Encode(), Fragment: b.Name}
		if scheme == "tuic" {
			share.User = url.UserPassword(user.UUID, user.Password)
		} else if scheme == "ss" {
			// SIP002 requires plain (percent-encoded) method:password userinfo
			// for AEAD-2022, not the base64url form used by older SS methods.
			share.User = url.UserPassword(inbound.Method, out["password"].(string))
		}
		link = share.String()
	}
	if err != nil {
		return "", nil, err
	}
	config, err := json.MarshalIndent(map[string]any{"outbounds": []any{out}}, "", "  ")
	if err != nil {
		return "", nil, exportError("客户端配置生成失败")
	}
	return link, config, nil
}

func exportError(message string) error {
	return fmt.Errorf("%w：%s", ErrClientExport, message)
}

func exportHost(host string) (string, error) {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
		if !strings.Contains(host, ":") || net.ParseIP(host) == nil {
			return "", exportError("节点 IPv6 连接地址无效")
		}
	}
	if host == "" || strings.ContainsAny(host, "/\\@?#[]%") || strings.IndexFunc(host, unicode.IsSpace) >= 0 || strings.IndexFunc(host, unicode.IsControl) >= 0 {
		return "", exportError("节点连接地址无效")
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return "", exportError("节点连接地址应为主机名或 IP，不包含端口")
	}
	return host, nil
}

func (b *Bundle) exportTLS(in *exportInbound, out map[string]any, query url.Values) error {
	if b.ClientParameters != nil {
		if err := b.ValidateClientParameters(*b.ClientParameters); err != nil {
			return err
		}
	}
	tls := in.TLS
	if tls == nil || !tls.Enabled {
		if in.Type == "anytls" || in.Type == "trojan" || in.Type == "hysteria2" || in.Type == "tuic" {
			return exportError("该协议的原节点配置缺少 TLS")
		}
		if in.Type == "vless" {
			query.Set("security", "none")
		}
		return nil
	}
	if in.Type == "shadowsocks" {
		return exportError("Shadowsocks 的附加 TLS 配置暂不支持导出")
	}
	if tls.ClientAuthentication != "" && tls.ClientAuthentication != "no" {
		return exportError("需要客户端证书的 TLS 配置不能自动导出")
	}
	if tls.ECH != nil && tls.ECH.Enabled || tls.MinVersion != "" || tls.MaxVersion != "" || nonemptyJSON(tls.CipherSuites) || nonemptyJSON(tls.CurvePreferences) {
		return exportError("该自定义 TLS 配置暂不支持节点链接导出")
	}
	clientTLS := map[string]any{"enabled": true}
	sni := tls.ServerName
	if b.ClientParameters != nil && b.ClientParameters.SNI != "" {
		sni = b.ClientParameters.SNI
	}
	reality := tls.Reality != nil && tls.Reality.Enabled
	if sni == "" && reality {
		sni = tls.Reality.Handshake.Server
	}
	// If an imported TLS inbound has no server_name, retain the client's
	// normal server-host verification behavior rather than inventing an SNI.
	if sni != "" {
		clientTLS["server_name"] = sni
		query.Set("sni", sni)
	}
	alpn, err := stringList(tls.ALPN)
	if err != nil {
		return exportError("TLS ALPN 配置格式无效")
	}
	if len(alpn) > 0 {
		for _, item := range alpn {
			if item == "" || strings.Contains(item, ",") {
				return exportError("TLS ALPN 无法完整表示为节点链接")
			}
		}
		clientTLS["alpn"] = alpn
		query.Set("alpn", strings.Join(alpn, ","))
	}
	if reality {
		if in.Type != "vless" || sni == "" {
			return exportError("Reality 协议或握手域名配置无效")
		}
		private, err := decodeKey(tls.Reality.PrivateKey)
		if err != nil || len(private) != 32 {
			return exportError("Reality 密钥配置无效")
		}
		public, err := curve25519.X25519(private, curve25519.Basepoint)
		if err != nil {
			return exportError("Reality 公钥生成失败")
		}
		shortIDs, err := stringList(tls.Reality.ShortID)
		if err != nil || len(shortIDs) == 0 {
			return exportError("Reality 缺少有效的短标识")
		}
		shortID := shortIDs[0]
		if _, err := hex.DecodeString(shortID); err != nil || len(shortID) > 16 {
			return exportError("Reality 短标识配置无效")
		}
		publicKey := base64.RawURLEncoding.EncodeToString(public)
		clientTLS["reality"] = map[string]any{"enabled": true, "public_key": publicKey, "short_id": shortID}
		clientTLS["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
		query.Set("security", "reality")
		query.Set("fp", "chrome")
		query.Set("pbk", publicKey)
		query.Set("sid", shortID)
	} else {
		// Preserve the panel-generated bundle's explicit insecure export choice.
		// A legacy certificate_path alone does not establish a self-signed
		// certificate: adopted nodes keep certificate verification enabled.
		insecure := templateInsecure(b.LinkTemplate)
		if b.ClientParameters != nil {
			insecure = b.ClientParameters.Insecure
			if fp := b.ClientParameters.Fingerprint; fp != "" {
				clientTLS["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
				query.Set("fp", fp)
			}
		}
		clientTLS["insecure"] = insecure
		if in.Type == "vless" || in.Type == "trojan" {
			query.Set("security", "tls")
		}
		if insecure {
			if in.Type == "vless" || in.Type == "trojan" {
				query.Set("allowInsecure", "1")
			} else {
				query.Set("insecure", "1")
			}
		}
	}
	out["tls"] = clientTLS
	return nil
}

func templateInsecure(template string) bool {
	u, err := url.Parse(template)
	if err != nil {
		return false
	}
	query := u.Query()
	return query.Get("insecure") == "1" || query.Get("allowInsecure") == "1"
}

func stringList(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err != nil {
		return nil, err
	}
	return []string{single}, nil
}

func decodeKey(value string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if key, err := enc.DecodeString(value); err == nil {
			return key, nil
		}
	}
	return nil, errors.New("invalid key")
}

func nonemptyJSON(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "[]", "{}":
		return false
	default:
		return true
	}
}

func exportTransport(in *exportInbound, out map[string]any, query url.Values) error {
	if !nonemptyJSON(in.Transport) {
		if in.Type == "vless" || in.Type == "trojan" || in.Type == "vmess" {
			query.Set("type", "tcp")
		}
		return nil
	}
	if in.Type != "vless" && in.Type != "trojan" && in.Type != "vmess" {
		return exportError("该协议的传输配置暂不支持导出")
	}
	var transport map[string]json.RawMessage
	if err := json.Unmarshal(in.Transport, &transport); err != nil {
		return exportError("传输配置格式无效")
	}
	var kind string
	if err := json.Unmarshal(transport["type"], &kind); err != nil {
		return exportError("传输配置缺少有效类型")
	}
	client := map[string]any{"type": kind}
	query.Set("type", kind)
	allowed := map[string]bool{"type": true}
	switch kind {
	case "ws":
		allowed["path"], allowed["headers"] = true, true
		var path string
		if value, ok := transport["path"]; ok {
			if err := json.Unmarshal(value, &path); err != nil {
				return exportError("WebSocket 路径配置无效")
			}
			client["path"] = path
			query.Set("path", path)
		}
		var headers map[string]string
		if value, ok := transport["headers"]; ok {
			if err := json.Unmarshal(value, &headers); err != nil {
				return exportError("WebSocket 请求头配置无效")
			}
			for key, value := range headers {
				if !strings.EqualFold(key, "Host") {
					return exportError("自定义 WebSocket 请求头无法完整表示为节点链接")
				}
				query.Set("host", value)
			}
			if len(headers) > 0 {
				client["headers"] = headers
			}
		}
	case "grpc":
		allowed["service_name"] = true
		var serviceName string
		if value, ok := transport["service_name"]; ok {
			if err := json.Unmarshal(value, &serviceName); err != nil {
				return exportError("gRPC 服务名称配置无效")
			}
			client["service_name"] = serviceName
			query.Set("serviceName", serviceName)
		}
	default:
		return exportError("该传输方式暂不支持节点链接导出")
	}
	for key := range transport {
		if !allowed[key] {
			return exportError("该自定义传输参数无法完整表示为节点链接")
		}
	}
	out["transport"] = client
	return nil
}

func vmessShare(out map[string]any, user exportUser, query url.Values, name string) (string, error) {
	network := query.Get("type")
	if network == "grpc" {
		return "", exportError("VMess gRPC 暂不支持完整节点链接导出")
	}
	share := map[string]any{
		"v": "2", "ps": name, "add": out["server"], "port": strconv.Itoa(out["server_port"].(int)),
		"id": user.UUID, "aid": strconv.Itoa(user.AlterID), "scy": "auto", "net": network, "type": "none",
	}
	if network == "ws" {
		share["path"], share["host"] = query.Get("path"), query.Get("host")
	}
	if tls, ok := out["tls"].(map[string]any); ok {
		share["tls"], share["sni"] = "tls", query.Get("sni")
		if alpn := query.Get("alpn"); alpn != "" {
			share["alpn"] = alpn
		}
		share["allowInsecure"] = tls["insecure"]
		if fp := query.Get("fp"); fp != "" {
			share["fp"] = fp
		}
	}
	raw, err := json.Marshal(share)
	if err != nil {
		return "", exportError("VMess 链接生成失败")
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(raw), nil
}
