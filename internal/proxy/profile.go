package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

const profileLimit = 512 << 10
const tunDeviceName = "KeyDockTun"

// Decode only fields that KeyDock's SS2022 feed is allowed to control.
// Credentials are passed to the core on stdin. They never enter logs or argv.
type proxyNode struct {
	Name     string `yaml:"name"`
	Type     string `yaml:"type"`
	Server   string `yaml:"server"`
	Port     int    `yaml:"port"`
	Cipher   string `yaml:"cipher"`
	Password string `yaml:"password"`
	UDP      bool   `yaml:"udp,omitempty"`
}
type proxyGroup struct {
	Name     string   `yaml:"name"`
	Type     string   `yaml:"type"`
	Proxies  []string `yaml:"proxies"`
	URL      string   `yaml:"url,omitempty"`
	Interval int      `yaml:"interval,omitempty"`
}
type routing struct {
	Proxies []proxyNode  `yaml:"proxies"`
	Groups  []proxyGroup `yaml:"proxy-groups,omitempty"`
	Rules   []string     `yaml:"rules"`
}

type tunSettings struct {
	Enable              bool     `yaml:"enable"`
	Device              string   `yaml:"device"`
	Stack               string   `yaml:"stack"`
	AutoRoute           bool     `yaml:"auto-route"`
	AutoDetectInterface bool     `yaml:"auto-detect-interface"`
	StrictRoute         bool     `yaml:"strict-route"`
	DNSMode             string   `yaml:"dns-mode"`
	DNSHijack           []string `yaml:"dns-hijack"`
}

type snifferSettings struct {
	Enable              bool                    `yaml:"enable"`
	ParsePureIP         bool                    `yaml:"parse-pure-ip"`
	OverrideDestination bool                    `yaml:"override-destination"`
	Sniff               map[string]snifferPorts `yaml:"sniff"`
}

type snifferPorts struct {
	Ports []int `yaml:"ports"`
}

func (m *Manager) subscription(ctx context.Context, raw string, tlsConfig *tls.Config) (_ []byte, resultErr error) {
	started := time.Now()
	status := 0
	var cause error
	defer func() {
		if cause == nil {
			cause = resultErr
		}
		m.logHTTPStage("proxy.subscription", time.Since(started), cause, status, http.StatusOK)
	}()
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("订阅地址必须为有效的 HTTPS 地址")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.TLSClientConfig = tlsConfig
	defer tr.CloseIdleConnections()
	c := http.Client{Transport: tr, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, errors.New("订阅地址无效")
	}
	resp, err := c.Do(req)
	if err != nil {
		cause = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("无法连接订阅服务，请检查网络后重试")
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	if status != http.StatusOK {
		return nil, errors.New("订阅服务返回 HTTP " + strconv.Itoa(status))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, profileLimit+1))
	if err != nil || len(body) > profileLimit {
		cause = err
		return nil, errors.New("读取代理订阅失败或配置体积过大")
	}
	return body, nil
}

func validLabel(s string) bool {
	return len(s) > 0 && len(s) <= 256 && strings.TrimSpace(s) == s && !strings.Contains(s, ",") && strings.IndexFunc(s, unicode.IsControl) < 0
}
func validHost(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(s, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}
func validNode(n proxyNode) bool {
	if !validLabel(n.Name) || n.Type != "ss" || n.Cipher != "2022-blake3-aes-128-gcm" || !validHost(n.Server) || n.Port < 1 || n.Port > 65535 {
		return false
	}
	keys := strings.Split(n.Password, ":")
	if len(keys) != 2 {
		return false
	}
	for _, key := range keys {
		data, err := base64.StdEncoding.DecodeString(key)
		if err != nil || len(data) != 16 {
			return false
		}
	}
	return true
}
func validRule(rule string, names map[string]bool) bool {
	p := strings.Split(rule, ",")
	for i := range p {
		p[i] = strings.TrimSpace(p[i])
		if !validLabel(p[i]) {
			return false
		}
	}
	if len(p) == 2 {
		return p[0] == "MATCH" && names[p[1]]
	}
	if len(p) < 3 || len(p) > 4 || !names[p[2]] {
		return false
	}
	if len(p) == 4 && (p[3] != "no-resolve" || p[0] != "IP-CIDR" && p[0] != "IP-CIDR6" && p[0] != "GEOIP") {
		return false
	}
	switch p[0] {
	case "DOMAIN", "DOMAIN-SUFFIX":
		return validHost(p[1])
	case "DOMAIN-KEYWORD":
		return !strings.ContainsAny(p[1], "/\\")
	case "IP-CIDR", "IP-CIDR6":
		network, err := netip.ParsePrefix(p[1])
		return err == nil && network.Addr().Is4() == (p[0] == "IP-CIDR")
	case "GEOSITE", "GEOIP":
		for _, r := range p[1] {
			if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return false
			}
		}
		return true
	}
	return false
}
func buildProfile(body []byte, port int) ([]byte, error) {
	return buildModeProfile(body, port, ModeSystem)
}

func buildModeProfile(body []byte, port int, mode Mode) ([]byte, error) {
	return buildRoutingProfile(body, port, false, mode)
}

func buildRouting(body []byte, port int, diagnostic bool) ([]byte, error) {
	return buildRoutingProfile(body, port, diagnostic, ModeSystem)
}

func buildRoutingProfile(body []byte, port int, diagnostic bool, mode Mode) ([]byte, error) {
	if mode != ModeSystem && mode != ModeTUN {
		return nil, errors.New("代理模式无效")
	}
	bad := errors.New("订阅不是受支持的 KeyDock SS2022 分流配置，请重新下载连接包")
	if len(body) > profileLimit || port < 1 || port > 65535 {
		return nil, bad
	}
	var cfg routing
	dec := yaml.NewDecoder(bytes.NewReader(body))
	if dec.Decode(&cfg) != nil || dec.Decode(new(yaml.Node)) != io.EOF || len(cfg.Proxies) == 0 || len(cfg.Proxies) > 128 || len(cfg.Groups) > 128 || len(cfg.Rules) == 0 || len(cfg.Rules) > 8192 {
		return nil, bad
	}
	names := map[string]bool{"DIRECT": true, "REJECT": true}
	for _, node := range cfg.Proxies {
		if !validNode(node) || names[node.Name] {
			return nil, bad
		}
		names[node.Name] = true
	}
	for i := range cfg.Groups {
		g := &cfg.Groups[i]
		if !validLabel(g.Name) || names[g.Name] || len(g.Proxies) == 0 {
			return nil, bad
		}
		switch g.Type {
		case "select":
			g.URL = ""
			g.Interval = 0
		case "url-test", "fallback":
			// The s-ui Auto group uses a connectivity test; never contact a URL supplied by the feed.
			g.URL = "https://www.gstatic.com/generate_204"
			g.Interval = 300
		default:
			return nil, bad
		}
		names[g.Name] = true
	}
	for _, g := range cfg.Groups {
		for _, name := range g.Proxies {
			if !names[name] || name == g.Name {
				return nil, bad
			}
		}
	}
	for _, rule := range cfg.Rules {
		if !validRule(rule, names) {
			return nil, bad
		}
	}
	if diagnostic {
		// A direct fallback could make a disabled device look healthy. Put only
		// the first device SS2022 node into this short-lived probe's profile.
		cfg.Proxies = cfg.Proxies[:1]
		cfg.Groups = nil
		cfg.Rules = []string{"MATCH," + cfg.Proxies[0].Name}
	}
	profile := struct {
		Routing       routing          `yaml:",inline"`
		Port          int              `yaml:"mixed-port"`
		Bind          string           `yaml:"bind-address"`
		AllowLAN      bool             `yaml:"allow-lan"`
		Mode          string           `yaml:"mode"`
		Log           string           `yaml:"log-level"`
		GeoAutoUpdate bool             `yaml:"geo-auto-update"`
		IPv6          bool             `yaml:"ipv6,omitempty"`
		TUN           *tunSettings     `yaml:"tun,omitempty"`
		Sniffer       *snifferSettings `yaml:"sniffer,omitempty"`
	}{Routing: cfg, Port: port, Bind: "127.0.0.1", Mode: "rule", Log: "silent"}
	if mode == ModeTUN {
		// Mihomo checks for usable host IPv6 before adding the IPv6 TUN route.
		profile.IPv6 = true
		profile.TUN = &tunSettings{
			Enable: true, Device: tunDeviceName, Stack: "mips", AutoRoute: true, AutoDetectInterface: true,
			// Keep Windows DNS and more-specific LAN/VPN routes in place.
			DNSMode: "disabled", DNSHijack: []string{}, StrictRoute: false,
		}
		// Use HTTP Host or TLS/QUIC SNI for domain rules without replacing the
		// resolver's destination IP, which still drives the IP rules and dial.
		profile.Sniffer = &snifferSettings{
			Enable: true, ParsePureIP: true, OverrideDestination: false,
			Sniff: map[string]snifferPorts{"HTTP": {Ports: []int{80}}, "TLS": {Ports: []int{443, 8443}}, "QUIC": {Ports: []int{443, 8443}}},
		}
	}
	return yaml.Marshal(profile)
}
