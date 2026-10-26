// Package subscription renders dedicated client configurations without copying
// server-side secrets or silently downgrading unsupported transports.
package subscription

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type Node struct {
	Name, Link string
	Config     json.RawMessage
}
type Result struct {
	Content       []byte
	ContentType   string
	Unsupported   []string
	IncludedCount int
	Name          string
	Usage         Usage
}

var ErrNoCompatible = errors.New("此格式没有兼容节点")
var ErrFormat = errors.New("不支持的订阅格式")

type outbound struct {
	Type       string          `json:"type"`
	Server     string          `json:"server"`
	Port       int             `json:"server_port"`
	Method     string          `json:"method"`
	Password   string          `json:"password"`
	UUID       string          `json:"uuid"`
	Security   string          `json:"security"`
	AlterID    int             `json:"alter_id"`
	Flow       string          `json:"flow"`
	Congestion string          `json:"congestion_control"`
	TLS        tlsOptions      `json:"tls"`
	Transport  json.RawMessage `json:"transport"`
	Obfs       json.RawMessage `json:"obfs"`
}
type tlsOptions struct {
	Enabled    bool     `json:"enabled"`
	ServerName string   `json:"server_name"`
	Insecure   bool     `json:"insecure"`
	ALPN       []string `json:"alpn"`
	UTLS       struct {
		Enabled     bool   `json:"enabled"`
		Fingerprint string `json:"fingerprint"`
	} `json:"utls"`
	Reality struct {
		Enabled   bool   `json:"enabled"`
		PublicKey string `json:"public_key"`
		ShortID   string `json:"short_id"`
	} `json:"reality"`
}

func Render(format string, nodes []Node) (Result, error) {
	r := Result{ContentType: "text/plain; charset=utf-8", Unsupported: []string{}}
	if format != "clash" && format != "base64" && format != "shadowrocket" && format != "loon" && format != "qx" {
		return r, ErrFormat
	}
	lines := []string{}
	proxies := []map[string]any{}
	names := []string{}
	seen := map[string]int{}
	for i, n := range nodes {
		name := strings.Join(strings.Fields(n.Name), " ")
		if name == "" {
			name = fmt.Sprintf("节点%d", i+1)
		}
		rr := []rune(name)
		if len(rr) > 80 {
			name = string(rr[:80])
		}
		base := name
		if base == "订阅节点" {
			base += " (节点)"
		}
		name = base
		for suffix := 2; seen[name] > 0; suffix++ {
			name = fmt.Sprintf("%s (%d)", base, suffix)
		}
		seen[name] = 1
		var cfg struct {
			Outbounds []outbound `json:"outbounds"`
		}
		if json.Unmarshal(n.Config, &cfg) != nil || len(cfg.Outbounds) != 1 {
			r.Unsupported = append(r.Unsupported, name+"：客户端配置无效")
			continue
		}
		o := cfg.Outbounds[0]
		if o.Server == "" || strings.ContainsAny(o.Server, "\r\n\t /\\,@") || o.Port < 1 || o.Port > 65535 {
			r.Unsupported = append(r.Unsupported, name+"：服务器地址无效")
			continue
		}
		var line string
		var proxy map[string]any
		var err error
		switch format {
		case "base64", "shadowrocket":
			u, e := url.Parse(n.Link)
			if e != nil || u == nil || strings.ContainsAny(n.Link, "\r\n\t ") || !supportedScheme(u.Scheme) {
				err = errors.New("分享链接无效")
			} else {
				line = n.Link
			}
		case "clash":
			proxy, err = clash(o, name)
		case "loon", "qx":
			line, err = mobile(o, name, format)
		}
		if err != nil {
			r.Unsupported = append(r.Unsupported, name+"："+err.Error())
			continue
		}
		r.IncludedCount++
		names = append(names, name)
		if format == "clash" {
			proxies = append(proxies, proxy)
		} else {
			lines = append(lines, line)
		}
	}
	if r.IncludedCount == 0 {
		return r, ErrNoCompatible
	}
	if format == "clash" {
		// JSON is valid YAML; serialization preserves quoting for arbitrary names and credentials.
		r.Content, _ = json.MarshalIndent(map[string]any{"mixed-port": 7890, "mode": "rule", "proxies": proxies, "proxy-groups": []any{map[string]any{"name": "订阅节点", "type": "select", "proxies": names}}, "rules": []string{"MATCH,订阅节点"}}, "", "  ")
		r.ContentType = "application/yaml; charset=utf-8"
	} else {
		raw := strings.Join(lines, "\n") + "\n"
		if format == "base64" || format == "shadowrocket" {
			raw = base64.StdEncoding.EncodeToString([]byte(raw))
		}
		r.Content = []byte(raw)
	}
	return r, nil
}
// Empty renders a subscription with no nodes in it that clients still accept,
// so they keep reading its usage after every node has run out. Clash gets a
// profile that goes direct rather than one that rejects everything: a person
// whose nodes all lapsed should lose the proxy, not their whole connection.
func Empty(format string) (Result, error) {
	r := Result{ContentType: "text/plain; charset=utf-8", Unsupported: []string{}}
	switch format {
	case "clash":
		r.Content, _ = json.MarshalIndent(map[string]any{"mixed-port": 7890, "mode": "rule", "proxies": []any{}, "proxy-groups": []any{map[string]any{"name": "订阅节点", "type": "select", "proxies": []string{"DIRECT"}}}, "rules": []string{"MATCH,订阅节点"}}, "", "  ")
		r.ContentType = "application/yaml; charset=utf-8"
	case "base64", "shadowrocket", "loon", "qx":
		r.Content = []byte{}
	default:
		return r, ErrFormat
	}
	return r, nil
}
func supportedScheme(s string) bool {
	switch s {
	case "ss", "vless", "vmess", "trojan", "anytls", "hysteria2", "hy2", "tuic":
		return true
	}
	return false
}
func extra(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != "{}"
}
func clash(o outbound, name string) (map[string]any, error) {
	if extra(o.Transport) || extra(o.Obfs) {
		return nil, errors.New("暂不转换自定义传输或混淆")
	}
	p := map[string]any{"name": name, "type": o.Type, "server": o.Server, "port": o.Port, "udp": true}
	switch o.Type {
	case "shadowsocks":
		if o.Password == "" || o.Method == "" {
			return nil, errors.New("缺少连接参数")
		}
		p["type"] = "ss"
		p["cipher"] = o.Method
		p["password"] = o.Password
	case "vmess":
		if o.UUID == "" {
			return nil, errors.New("缺少连接参数")
		}
		p["uuid"] = o.UUID
		p["alterId"] = o.AlterID
		p["cipher"] = "auto"
	case "vless":
		if o.UUID == "" {
			return nil, errors.New("缺少连接参数")
		}
		p["uuid"] = o.UUID
		if o.Flow != "" {
			p["flow"] = o.Flow
		}
	case "trojan", "anytls", "hysteria2":
		if o.Password == "" {
			return nil, errors.New("缺少连接参数")
		}
		p["password"] = o.Password
	case "tuic":
		if o.UUID == "" || o.Password == "" {
			return nil, errors.New("缺少连接参数")
		}
		p["uuid"] = o.UUID
		p["password"] = o.Password
		if o.Congestion != "" {
			p["congestion-controller"] = o.Congestion
		}
	default:
		return nil, errors.New("暂不支持该协议")
	}
	if o.TLS.Enabled {
		p["tls"] = true
		p["skip-cert-verify"] = o.TLS.Insecure
		if o.Type == "vmess" || o.Type == "vless" {
			p["servername"] = o.TLS.ServerName
		} else {
			p["sni"] = o.TLS.ServerName
		}
		if len(o.TLS.ALPN) > 0 {
			p["alpn"] = o.TLS.ALPN
		}
		if o.TLS.UTLS.Enabled {
			p["client-fingerprint"] = o.TLS.UTLS.Fingerprint
		}
		if o.TLS.Reality.Enabled {
			p["reality-opts"] = map[string]any{"public-key": o.TLS.Reality.PublicKey, "short-id": o.TLS.Reality.ShortID}
		}
	}
	return p, nil
}

// Line formats are intentionally conservative: reject rather than change a
// credential, fingerprint, or unsupported option to fit another client's grammar.
func mobile(o outbound, name, format string) (string, error) {
	if extra(o.Transport) || extra(o.Obfs) || (format == "qx" && (o.TLS.Reality.Enabled || o.Flow != "")) {
		return "", errors.New("此格式暂不转换 Reality、流控或自定义传输")
	}
	if format == "qx" && len(o.TLS.ALPN) > 0 {
		return "", errors.New("此格式暂不转换自定义 ALPN")
	}
	for _, v := range []string{name, o.Server, o.Password, o.UUID, o.TLS.ServerName, o.Method} {
		if strings.ContainsAny(v, "\r\n,\"#;") {
			return "", errors.New("字段含此格式无法安全表示的分隔符")
		}
	}
	if strings.ContainsAny(name+o.Server+o.TLS.ServerName, "=") {
		return "", errors.New("字段含此格式无法安全表示的分隔符")
	}
	host := o.Server
	if net.ParseIP(host) != nil && strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	port := strconv.Itoa(o.Port)
	if format == "loon" {
		return loon(o, name, host, port)
	}
	prefix := o.Type + "=" + net.JoinHostPort(o.Server, port)
	switch o.Type {
	case "shadowsocks":
		if o.Method != "2022-blake3-aes-128-gcm" && o.Method != "2022-blake3-aes-256-gcm" {
			return "", errors.New("QX 格式暂不支持该加密方法")
		}
		prefix += ", method=" + o.Method + ", password=" + o.Password
	case "vmess":
		if o.UUID == "" || o.AlterID != 0 {
			return "", errors.New("不支持该 VMess 参数")
		}
		prefix += ", method=aes-128-gcm, password=" + o.UUID
		if o.TLS.Enabled {
			prefix += ", obfs=over-tls, obfs-host=" + o.TLS.ServerName
		}
	case "vless":
		if o.UUID == "" {
			return "", errors.New("缺少连接参数")
		}
		prefix += ", password=" + o.UUID
		if o.TLS.Enabled {
			prefix += ", obfs=over-tls, obfs-host=" + o.TLS.ServerName
		}
	case "trojan", "anytls":
		if !o.TLS.Enabled || o.Password == "" {
			return "", errors.New("缺少 TLS 连接参数")
		}
		prefix += ", password=" + o.Password + ", over-tls=true, tls-host=" + o.TLS.ServerName
	default:
		return "", errors.New("QX 格式暂不输出此协议")
	}
	if o.TLS.Enabled {
		prefix += fmt.Sprintf(", tls-verification=%t", !o.TLS.Insecure)
	}
	return prefix + ", udp-relay=true, tag=" + name, nil
}

func loon(o outbound, name, host, port string) (string, error) {
	args := []string{}
	prefix := name + " = "
	switch o.Type {
	case "shadowsocks":
		if o.Password == "" || (o.Method != "2022-blake3-aes-128-gcm" && o.Method != "2022-blake3-aes-256-gcm") {
			return "", errors.New("Loon 暂不支持该加密方法")
		}
		prefix += "Shadowsocks," + host + "," + port + "," + o.Method + ",\"" + o.Password + "\""
	case "vmess":
		if o.UUID == "" || o.AlterID != 0 {
			return "", errors.New("不支持该 VMess 参数")
		}
		prefix += "VMess," + host + "," + port + ",aes-128-gcm,\"" + o.UUID + "\",transport=tcp,alterId=0"
	case "vless":
		if o.UUID == "" {
			return "", errors.New("缺少连接参数")
		}
		prefix += "VLESS," + host + "," + port + ",\"" + o.UUID + "\",transport=tcp"
		if o.Flow != "" {
			if o.Flow != "xtls-rprx-vision" {
				return "", errors.New("不支持该流控")
			}
			args = append(args, "flow="+o.Flow)
		}
	case "trojan", "anytls", "hysteria2":
		if !o.TLS.Enabled || o.Password == "" {
			return "", errors.New("缺少 TLS 连接参数")
		}
		kind := map[string]string{"trojan": "Trojan", "anytls": "AnyTLS", "hysteria2": "Hysteria2"}[o.Type]
		prefix += kind + "," + host + "," + port + ",\"" + o.Password + "\""
	default:
		return "", errors.New("Loon 格式暂不输出此协议")
	}
	if o.TLS.Enabled {
		args = append(args, "over-tls=true", "sni="+o.TLS.ServerName, fmt.Sprintf("skip-cert-verify=%t", o.TLS.Insecure))
		if o.TLS.UTLS.Enabled {
			if strings.ContainsAny(o.TLS.UTLS.Fingerprint, "\r\n,=\"#;") {
				return "", errors.New("无效指纹参数")
			}
			args = append(args, "tls-profile="+o.TLS.UTLS.Fingerprint)
		}
		for _, alpn := range o.TLS.ALPN {
			if strings.ContainsAny(alpn, "\r\n,=\"#;") {
				return "", errors.New("无效 ALPN 参数")
			}
		}
		if len(o.TLS.ALPN) > 0 {
			args = append(args, "alpn=\""+strings.Join(o.TLS.ALPN, ",")+"\"")
		}
		if o.TLS.Reality.Enabled {
			for _, v := range []string{o.TLS.Reality.PublicKey, o.TLS.Reality.ShortID} {
				if strings.ContainsAny(v, "\r\n,=\"#;") {
					return "", errors.New("无效 Reality 参数")
				}
			}
			args = append(args, "public-key=\""+o.TLS.Reality.PublicKey+"\"", "short-id="+o.TLS.Reality.ShortID)
		}
	}
	args = append(args, "udp=true")
	return prefix + "," + strings.Join(args, ","), nil
}
