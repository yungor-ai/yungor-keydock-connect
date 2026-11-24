package proxy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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
	raw, err := buildProfile([]byte(fixture), 29901)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(core, "-t", "-d", dir, "-f", "-")
	cmd.Stdin = bytes.NewReader(raw)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("synthetic config rejected: %v %s", err, output)
	}
}
