//go:build darwin && arm64

package proxy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDarwinTUNProfileOwnsRoutingSettings(t *testing.T) {
	subscription := []byte(fixture + `tun: {enable: false, device: utun1, strict-route: true, dns-hijack: ["any:53"]}
dns: {enable: true, nameserver: ["https://untrusted.invalid/dns-query"]}
mixed-port: 7890
external-controller: 0.0.0.0:9999
allow-lan: true
`)
	for _, ipv6 := range []bool{false, true} {
		config, err := buildDarwinTUNProfile(subscription, ipv6)
		if err != nil {
			t.Fatal(err)
		}
		var profile map[string]any
		if err := yaml.Unmarshal(config, &profile); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"dns", "external-controller"} {
			if _, found := profile[key]; found {
				t.Errorf("subscription controlled %q", key)
			}
		}
		if profile["mixed-port"] != 0 || profile["allow-lan"] != false {
			t.Fatalf("TUN exposed a listener: %#v", profile)
		}
		wantAddresses := []any{}
		if ipv6 {
			wantAddresses = []any{"fdfe:dcba:9876::1/126"}
		}
		tun := profile["tun"].(map[string]any)
		if tun["device"] != darwinTUNDevice || tun["dns-mode"] != "disabled" || tun["auto-route"] != true ||
			!reflect.DeepEqual(tun["dns-hijack"], []any{}) || !reflect.DeepEqual(tun["inet6-address"], wantAddresses) {
			t.Fatalf("unexpected TUN settings for IPv6=%t: %#v", ipv6, tun)
		}
	}
}

func TestDarwinRouteOwnershipUsesTheExactInterface(t *testing.T) {
	const routes = `Routing tables

Internet:
Destination        Gateway            Flags               Netif Expire
default            link#25            UCSg                utun5
default            192.168.8.1        UGScIg              en0
default            link#24            UCSIg               utun4

Internet6:
Destination                   Gateway                       Flags    Netif Expire
default                       fe80::%utun123               UGcIg    utun123
`
	if !darwinRouteTableContains(routes, "utun5") || !darwinRouteTableContains(routes, "utun123") {
		t.Fatal("an owned route was missed")
	}
	if darwinRouteTableContains(routes, "utun1") {
		t.Fatal("a different interface matched by prefix")
	}
}

func TestDarwinTUNProfileAcceptedByMihomo(t *testing.T) {
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
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, ipv6 := range []bool{false, true} {
		config, err := buildDarwinTUNProfile([]byte(fixture), ipv6)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), core, "-t", "-d", dir, "-f", "-")
		cmd.Stdin = bytes.NewReader(config)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Mihomo rejected the synthetic TUN profile for IPv6=%t: %v %s", ipv6, err, output)
		}
	}
}
