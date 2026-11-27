package proxy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// s-ui's actual group layout is Proxy -> [Auto, device] and Auto -> [device].
// All credentials in this fixture are synthetic, with a documentation-only IP.
const fixture = `proxies:
  - name: test-node
    type: ss
    server: 192.0.2.1
    port: 24443
    cipher: 2022-blake3-aes-128-gcm
    password: "AAAAAAAAAAAAAAAAAAAAAA==:AQEBAQEBAQEBAQEBAQEBAQ=="
    udp: true
proxy-groups:
  - {name: Proxy, type: select, proxies: [Auto, test-node]}
  - {name: Auto, type: url-test, proxies: [test-node], url: "https://untrusted.invalid", interval: 1}
rules:
  - DOMAIN,blocked.example,REJECT
  - IP-CIDR,10.0.0.0/8,DIRECT,no-resolve
  - IP-CIDR6,fc00::/7,DIRECT,no-resolve
  - GEOSITE,cn,DIRECT
  - GEOIP,CN,DIRECT
  - MATCH,Proxy
`

func TestProfilePreservesSplitAndStripsRuntimeSettings(t *testing.T) {
	source := fixture + `external-controller: 0.0.0.0:9999
listeners: [{type: mixed, port: 8888}]
tun: {enable: true}
script: {code: example}
allow-lan: true
geox-url: {geoip: "https://untrusted.invalid/"}
`
	raw, err := buildProfile([]byte(source), 23456)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["mode"] != "rule" || cfg["allow-lan"] != false || cfg["bind-address"] != "127.0.0.1" || cfg["mixed-port"] != 23456 {
		t.Fatal("incorrect listener or mode")
	}
	for _, key := range []string{"external-controller", "tun", "listeners", "script", "geox-url"} {
		if _, found := cfg[key]; found {
			t.Fatal(key)
		}
	}
	if bytes.Contains(raw, []byte("untrusted.invalid")) {
		t.Fatal("unexpected network target")
	}
	var before, after routing
	yaml.Unmarshal([]byte(fixture), &before)
	yaml.Unmarshal(raw, &after)
	if strings.Join(after.Rules, "\n") != strings.Join(before.Rules, "\n") || after.Proxies[0] != before.Proxies[0] || !strings.Contains(string(raw), "Auto") {
		t.Fatal("rules, groups or credentials changed")
	}
}
func TestRejectsInvalidFeedWithoutLeakingContents(t *testing.T) {
	for _, bad := range []string{
		strings.ReplaceAll(fixture, "MATCH,Proxy", "MATCH,unavailable"),
		strings.ReplaceAll(fixture, "GEOSITE,cn,DIRECT", "RULE-SET,external,DIRECT"),
		strings.ReplaceAll(fixture, "AAAAAAAAAAAAAAAAAAAAAA==", "bad-private-key"),
		fixture + "---\nrules: []\n",
		strings.ReplaceAll(fixture, "type: ss", "type: vmess"),
		"proxies: [",
	} {
		_, err := buildProfile([]byte(bad), 11111)
		if err == nil || strings.Contains(err.Error(), "private-key") {
			t.Fatal("invalid subscription was accepted or logged")
		}
	}
}

func TestMacMixedListenerUsesIPv4(t *testing.T) {
	raw, err := buildModeProfile([]byte(fixture+"ipv6: true\n"), 17890, ModeLocal)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["ipv6"] != false {
		t.Fatal("the macOS mixed listener must explicitly disable core IPv6")
	}
}

func TestTUNProfilePreservesRoutingAndUsesTrustedRuntimeSettings(t *testing.T) {
	source := []byte(fixture + `tun: {enable: false, device: "untrusted-device", strict-route: false, dns-hijack: ["any:53"], mtu: 9000}
dns: {enable: true, nameserver: ["https://untrusted.invalid/dns-query"]}
sniffer: {enable: false, override-destination: true}
ipv6: true
interface-name: untrusted-interface
external-controller: 0.0.0.0:9999
allow-lan: true
`)
	baseline, err := buildModeProfile(source, 23456, ModeSystem)
	if err != nil {
		t.Fatal(err)
	}
	tunnel, err := buildModeProfile(source, 23456, ModeTUN)
	if err != nil {
		t.Fatal(err)
	}
	var system, tun map[string]any
	if err := yaml.Unmarshal(baseline, &system); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(tunnel, &tun); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"interface-name", "external-controller"} {
		if _, found := tun[key]; found {
			t.Fatalf("feed runtime setting %q survived sanitization", key)
		}
	}
	if tun["ipv6"] != false {
		t.Fatal("Windows TUN must explicitly disable IPv6 because Mihomo defaults to enabled")
	}
	wantTUN := map[string]any{
		"enable": true, "device": "KeyDockTun", "stack": "mips", "auto-route": true,
		"auto-detect-interface": true, "strict-route": true, "dns-mode": "native", "dns-hijack": []any{}, "mtu": 1500,
	}
	if !reflect.DeepEqual(tun["tun"], wantTUN) {
		t.Fatalf("unexpected TUN settings: %v", tun["tun"])
	}
	wantSniffer := map[string]any{
		"enable": true, "parse-pure-ip": true, "override-destination": false,
		"sniff": map[string]any{
			"HTTP": map[string]any{"ports": []any{80}},
			"TLS":  map[string]any{"ports": []any{443, 8443}},
			"QUIC": map[string]any{"ports": []any{443, 8443}},
		},
	}
	if !reflect.DeepEqual(tun["sniffer"], wantSniffer) {
		t.Fatalf("unexpected domain sniffing settings: %v", tun["sniffer"])
	}
	if tun["log-level"] != "info" || system["log-level"] != "silent" {
		t.Fatal("only Windows TUN should emit its startup result")
	}
	delete(tun, "log-level")
	delete(system, "log-level")
	delete(tun, "tun")
	delete(tun, "sniffer")
	delete(tun, "ipv6")
	if tun["dns"] == nil || system["dns"] != nil {
		t.Fatal("only TUN should manage Windows DNS")
	}
	delete(tun, "dns")
	groups, ok := tun["proxy-groups"].([]any)
	if !ok || len(groups) == 0 {
		t.Fatal("missing TUN proxy groups")
	}
	// TUN appends one local group for DNS. Every subscription group must survive.
	tun["proxy-groups"] = groups[:len(groups)-1]
	if !reflect.DeepEqual(tun, system) {
		t.Fatal("TUN changed subscription routing or exposed the loopback listener")
	}
	if _, err := buildModeProfile([]byte(fixture), 23456, Mode("other")); err == nil {
		t.Fatal("unknown proxy mode accepted")
	}
}

func TestPriorSettingsOwnership(t *testing.T) {
	j := ownership{Prior: settings{Flags: 9, Server: "example:1234", Bypass: "old"}, Owned: settings{Flags: 3, Server: "127.0.0.1:32768", Bypass: "<local>"}}
	restored, ok := j.restore(j.Owned)
	if !ok || restored != j.Prior {
		t.Fatal("previous settings were lost")
	}
	external := settings{Flags: 3, Server: "example:8080", Bypass: "new"}
	if got, changed := j.restore(external); changed || got != external {
		t.Fatal("replaced someone else's endpoint")
	}
	modified := j.Owned
	modified.Bypass = "corporate"
	modified.Flags = 1
	got, ok := j.restore(modified)
	if !ok || got.Server != j.Prior.Server || got.Bypass != "corporate" || got.Flags != 1 {
		t.Fatal("external overrides were lost")
	}
}
func TestRealCoreAcceptsFilteredKeyDockConfig(t *testing.T) {
	core := os.Getenv("KEYDOCK_PROFILE_CORE")
	if core == "" {
		t.Skip("set KEYDOCK_PROFILE_CORE and KEYDOCK_GEO_DIR for standalone core validation")
	}
	dir := t.TempDir()
	for _, name := range []string{"GeoSite.dat", "geoip.metadb"} {
		data, err := os.ReadFile(filepath.Join(os.Getenv("KEYDOCK_GEO_DIR"), name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []Mode{ModeSystem, ModeTUN} {
		t.Run(string(mode), func(t *testing.T) {
			raw, err := buildModeProfile([]byte(fixture), 29901, mode)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(core, "-t", "-d", dir, "-f", "-")
			cmd.Stdin = bytes.NewReader(raw)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("synthetic config rejected: %v %s", err, output)
			}
		})
	}
}

func TestDiagnosticProfileForcesOnlyDeviceNode(t *testing.T) {
	source := strings.ReplaceAll(fixture, "MATCH,Proxy", "MATCH,DIRECT")
	encoded, err := buildRouting([]byte(source), 12345, true)
	if err != nil {
		t.Fatal(err)
	}
	var cfg routing
	if yaml.Unmarshal(encoded, &cfg) != nil || len(cfg.Proxies) != 1 || len(cfg.Groups) != 0 || len(cfg.Rules) != 1 || cfg.Rules[0] != "MATCH,test-node" {
		t.Fatal("diagnosis can bypass the device node or trigger an unrelated test URL")
	}
	if _, err = buildRouting([]byte(strings.ReplaceAll(source, "DOMAIN,blocked.example,REJECT", "RULE-SET,example,REJECT")), 12345, true); err == nil {
		t.Fatal("invalid subscription accepted as healthy")
	}
}

func TestWindowsTUNDNSUsesOnlyTrustedIPv4Upstreams(t *testing.T) {
	// Labels are valid subscription data but unsafe to embed in a DoH fragment.
	const node = "name&key=value"
	src := strings.ReplaceAll(fixture, "test-node", node)
	src = strings.ReplaceAll(src, "Auto", "KeyDock-DNS")
	src += "dns: {listen: '0.0.0.0:53', ipv6: true, nameserver: ['https://untrusted.invalid/dns-query']}\n"
	raw, err := buildModeProfile([]byte(src), 23456, ModeTUN)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		routing `yaml:",inline"`
		DNS     dnsSettings `yaml:"dns"`
	}
	if err = yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	dns := cfg.DNS
	if !dns.Enable || dns.IPv6 || dns.EnhancedMode != "fake-ip" || dns.FakeIPRange != "198.18.0.1/16" ||
		bytes.Contains(raw, []byte("untrusted.invalid")) || bytes.Contains(raw, []byte("listen:")) {
		t.Fatal("Windows TUN DNS must use the owned IPv4 configuration")
	}
	alias := cfg.Groups[len(cfg.Groups)-1]
	if alias.Name != "KeyDock-DNS-1" || alias.Type != "select" || !reflect.DeepEqual(alias.Proxies, []string{node}) ||
		!reflect.DeepEqual(dns.NameServer, []string{"https://1.1.1.1/dns-query#" + alias.Name, "https://8.8.8.8/dns-query#" + alias.Name}) {
		t.Fatal("public DNS must dial through a validated device without interpreting its label as URL options")
	}
	if len(dns.ProxyServerNameServer) == 0 || !reflect.DeepEqual(dns.ProxyServerNameServer, dns.DefaultNameServer) {
		t.Fatal("relay bootstrap must work before the relay is connected")
	}
}

func TestWindowsTUNDNSReturnsRealIPOnlyForEnrolledServer(t *testing.T) {
	const backend = "keydock.example.com"
	const feed = fixture + "dns: {fake-ip-filter: ['+.example.com']}\n"
	profileFilters := func(serverURL string) []string {
		t.Helper()
		raw, err := buildRoutingProfileWithTUN([]byte(feed), 23456, false, ModeTUN, nil, serverURL)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			DNS dnsSettings `yaml:"dns"`
		}
		if err = yaml.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg.DNS.FakeIPFilter
	}
	defaults := profileFilters("")
	if slices.Contains(defaults, "+.example.com") || slices.Contains(defaults, backend) {
		t.Fatal("the subscription must not choose DNS exceptions")
	}
	for _, tc := range []struct {
		server string
		want   []string
	}{
		{"https://KeyDock.Example.com.:8443/base", append(slices.Clone(defaults), backend)},
		{"https://192.0.2.11", defaults},
		{"https://[2001:db8::11]", defaults},
		{"https://*.example.com", defaults},
		{"https://user@keydock.example.com", defaults},
	} {
		if got := profileFilters(tc.server); !slices.Equal(got, tc.want) {
			t.Errorf("unexpected DNS exceptions for %q: %v", tc.server, got)
		}
	}
}
