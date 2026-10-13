package singbox

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ClientParameters are explicit client-only choices. They are sealed with the
// bundle, never written to the server inbound, and contain no credentials.
type ClientParameters struct {
	SNI         string `json:"sni"`
	Insecure    bool   `json:"insecure"`
	Fingerprint string `json:"fingerprint"`
}

// Every explicit profile specifies all three settings. Missing/null flags must
// not silently disable verification or erase a previously saved choice.
func (p *ClientParameters) UnmarshalJSON(raw []byte) error {
	var wire struct {
		SNI         *string `json:"sni"`
		Insecure    *bool   `json:"insecure"`
		Fingerprint *string `json:"fingerprint"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&wire) != nil || wire.SNI == nil || wire.Insecure == nil || wire.Fingerprint == nil {
		return exportError("客户端参数必须完整提供 SNI、证书校验和指纹设置")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return exportError("客户端参数格式无效")
	}
	*p = ClientParameters{SNI: *wire.SNI, Insecure: *wire.Insecure, Fingerprint: *wire.Fingerprint}
	return p.Validate()
}

func (p ClientParameters) Validate() error {
	if p.SNI != "" && !validServerName(p.SNI) {
		return exportError("SNI 必须是有效的主机名或 IP 地址")
	}
	switch p.Fingerprint {
	case "", "chrome", "firefox", "edge", "safari", "360", "qq", "ios", "android", "random", "randomized":
	default:
		return exportError("不支持的 TLS 指纹")
	}
	return nil
}

func validServerName(value string) bool {
	if len(value) == 0 || len(value) > 253 {
		return false
	}
	if net.ParseIP(value) != nil {
		return true
	}
	name := strings.TrimSuffix(value, ".")
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

// ValidateClientParameters ensures explicit choices can be faithfully exported.
func (b *Bundle) ValidateClientParameters(p ClientParameters) error {
	if err := p.Validate(); err != nil {
		return err
	}
	in, err := b.clientInbound()
	if err != nil {
		return err
	}
	if in.TLS == nil || !in.TLS.Enabled {
		return exportError("该节点未启用 TLS，不能设置 TLS 客户端参数")
	}
	if in.TLS.Reality != nil && in.TLS.Reality.Enabled {
		return exportError("Reality 不支持此客户端参数覆盖")
	}
	if in.Type == "shadowsocks" {
		return exportError("该协议不支持 TLS 客户端参数")
	}
	if p.Fingerprint != "" && (in.Type == "tuic" || in.Type == "hysteria2") {
		return exportError("QUIC 节点不支持 uTLS 指纹")
	}
	return nil
}

func (b *Bundle) clientInbound() (*exportInbound, error) {
	if b == nil {
		return nil, exportError("节点配置无效")
	}
	var server struct {
		Inbounds []exportInbound `json:"inbounds"`
	}
	if json.Unmarshal(b.Config, &server) != nil {
		return nil, exportError("原节点配置格式无效")
	}
	var found *exportInbound
	for i := range server.Inbounds {
		if server.Inbounds[i].Port == b.Port {
			if found != nil {
				return nil, exportError("同一端口存在多个入站配置")
			}
			found = &server.Inbounds[i]
		}
	}
	if found == nil {
		return nil, exportError("原配置中未找到节点入站")
	}
	return found, nil
}

// ImportAnyTLSLink copies only validated client parameters after verifying the
// node identity. The credential in the supplied link is never persisted.
func (b *Bundle) ImportAnyTLSLink(link string, hosts ...string) (*ClientParameters, error) {
	u, p, err := parseAnyTLSLink(link)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, exportError("节点配置无效")
	}
	port, _ := anyTLSLinkPort(u)
	if port != b.Port {
		return nil, exportError("原链接端口与节点不一致")
	}
	host := u.Hostname()
	matched := false
	for _, known := range hosts {
		known, err := exportHost(known)
		if err != nil {
			continue
		}
		if a, c := net.ParseIP(host), net.ParseIP(known); a != nil && c != nil {
			matched = matched || a.Equal(c)
		} else {
			matched = matched || strings.EqualFold(strings.TrimSuffix(host, "."), strings.TrimSuffix(known, "."))
		}
	}
	if !matched {
		return nil, exportError("原链接地址与节点不一致")
	}
	in, err := b.clientInbound()
	if err != nil {
		return nil, err
	}
	if in.Type != "anytls" || len(in.Users) != 1 || in.Users[0].Password == "" {
		return nil, exportError("原链接仅支持唯一用户的 AnyTLS 节点")
	}
	got, want := sha256.Sum256([]byte(u.User.Username())), sha256.Sum256([]byte(in.Users[0].Password))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		return nil, exportError("原链接凭据与节点不一致")
	}
	if err := b.ValidateClientParameters(*p); err != nil {
		return nil, err
	}
	return p, nil
}

// ValidateOriginalAnyTLSLink checks syntax and settings before acquiring SSH.
func ValidateOriginalAnyTLSLink(link string) error {
	_, _, err := parseAnyTLSLink(link)
	return err
}

func parseAnyTLSLink(link string) (*url.URL, *ClientParameters, error) {
	if len(link) == 0 || len(link) > 8192 || strings.TrimSpace(link) != link {
		return nil, nil, exportError("原链接格式无效")
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "anytls" || u.User == nil || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.ForceQuery {
		return nil, nil, exportError("请提供原 AnyTLS 节点链接")
	}
	if _, passwordField := u.User.Password(); passwordField {
		return nil, nil, exportError("原链接用户凭据格式无效")
	}
	port, err := anyTLSLinkPort(u)
	if err != nil || port < 1 || port > 65535 {
		return nil, nil, exportError("原链接端口无效")
	}
	_, err = exportHost(u.Hostname())
	if err != nil {
		return nil, nil, exportError("原链接地址无效")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, nil, exportError("原链接参数格式无效")
	}
	p := &ClientParameters{}
	for name, values := range query {
		if len(values) != 1 {
			return nil, nil, exportError("原链接存在重复参数")
		}
		switch name {
		case "sni":
			p.SNI = values[0]
		case "fp":
			p.Fingerprint = values[0]
		case "insecure":
			if values[0] != "0" && values[0] != "1" {
				return nil, nil, exportError("原链接证书校验参数必须为 0 或 1")
			}
			p.Insecure = values[0] == "1"
		default:
			return nil, nil, exportError("原链接包含暂不支持的客户端参数")
		}
	}
	if err := p.Validate(); err != nil {
		return nil, nil, err
	}
	return u, p, nil
}

func anyTLSLinkPort(u *url.URL) (int, error) {
	if u.Port() == "" {
		return 443, nil
	}
	return strconv.Atoi(u.Port())
}

// EffectiveClientParameters reflects the same outbound used for links and QR.
func (b *Bundle) EffectiveClientParameters(host string) (ClientParameters, error) {
	_, raw, err := b.ExportClient(host)
	if err != nil {
		return ClientParameters{}, err
	}
	var config struct {
		Outbounds []struct {
			TLS struct {
				ServerName string `json:"server_name"`
				Insecure   bool   `json:"insecure"`
				UTLS       struct {
					Fingerprint string `json:"fingerprint"`
				} `json:"utls"`
			} `json:"tls"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(raw, &config); err != nil || len(config.Outbounds) != 1 {
		return ClientParameters{}, exportError("客户端参数读取失败")
	}
	tls := config.Outbounds[0].TLS
	return ClientParameters{SNI: tls.ServerName, Insecure: tls.Insecure, Fingerprint: tls.UTLS.Fingerprint}, nil
}
