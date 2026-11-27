//go:build darwin && arm64

package proxy

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDarwinTUNProfileOwnsRoutingSettings(t *testing.T) {
	subscription := []byte(fixture + `ipv6: true
tun: {enable: false, device: utun1, strict-route: true, inet6-address: ["fd00::1/126"], dns-hijack: ["any:53"]}
dns: {enable: true, nameserver: ["https://untrusted.invalid/dns-query"]}
mixed-port: 7890
external-controller: 0.0.0.0:9999
allow-lan: true
`)
	config, err := buildDarwinTUNProfile(subscription)
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
	if profile["ipv6"] != false {
		t.Errorf("expected explicit ipv6: false, got %v", profile["ipv6"])
	}
	if profile["mixed-port"] != 0 || profile["allow-lan"] != false {
		t.Fatalf("TUN exposed a listener: %#v", profile)
	}
	tun := profile["tun"].(map[string]any)
	if tun["device"] != darwinTUNDevice || tun["dns-mode"] != "disabled" || tun["auto-route"] != true ||
		!reflect.DeepEqual(tun["dns-hijack"], []any{}) || !reflect.DeepEqual(tun["inet6-address"], []any{}) {
		t.Fatalf("unexpected TUN settings: %#v", tun)
	}
}

func TestDarwinTUNRequiresOnlyIPv4(t *testing.T) {
	for _, probe := range darwinTUNProbeAddresses {
		if !probe.Is4() {
			t.Errorf("startup requires an IPv6 route for %s", probe)
		}
	}
}

func TestDarwinMissingRoute(t *testing.T) {
	// macOS route writes this diagnostic to stderr even when its exit status is zero.
	for _, exit := range []error{nil, errors.New("exit status 1")} {
		if name, err := parseDarwinRouteInterface([]byte("route: writing to routing socket: not in table\n"), exit); name != "" || err != nil {
			t.Fatalf("missing route with exit %v: got %q, %v", exit, name, err)
		}
	}
	if _, err := parseDarwinRouteInterface([]byte("unexpected result"), nil); err == nil {
		t.Fatal("a malformed response cannot confirm route absence")
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
	config, err := buildDarwinTUNProfile([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), core, "-t", "-d", dir, "-f", "-")
	cmd.Stdin = bytes.NewReader(config)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Mihomo rejected the synthetic TUN profile %v %s", err, output)
	}
}

// Run as root on a disposable macOS runner. runDarwinTUN takes over the
// runner's IPv4 routes for the duration of the ready callback.
func TestDarwinTUNRouteLifecycle(t *testing.T) {
	if os.Getenv("KEYDOCK_DARWIN_TUN_E2E") != "1" {
		t.Skip("requires a disposable macOS runner")
	}
	core := os.Getenv("KEYDOCK_TEST_CORE")
	if os.Geteuid() != 0 || core == "" {
		t.Fatal("run as root with KEYDOCK_TEST_CORE pointing to the packaged core")
	}
	ipv6Probe := netip.MustParseAddr("2606:4700:4700::1111")
	readRoutes := func() map[netip.Addr]string {
		t.Helper()
		routes := map[netip.Addr]string{}
		for _, addr := range append(darwinTUNProbeAddresses[:], ipv6Probe) {
			name, err := darwinRouteInterface(t.Context(), addr)
			if err != nil {
				t.Errorf("route lookup for %s: %v", addr, err)
			}
			routes[addr] = name
		}
		return routes
	}
	before := readRoutes()
	// Route unrelated runner traffic directly while exercising real utun creation.
	subscription := strings.ReplaceAll(peerSubscription(1), "MATCH,test-node", "MATCH,DIRECT")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := runDarwinTUN(ctx, core, []byte(subscription), func() error {
		defer cancel()
		active := readRoutes()
		for _, addr := range darwinTUNProbeAddresses {
			if active[addr] != darwinTUNDevice {
				t.Errorf("IPv4 %s uses %q", addr, active[addr])
			}
		}
		if active[ipv6Probe] == darwinTUNDevice || active[ipv6Probe] != before[ipv6Probe] {
			t.Errorf("IPv6 route changed from %q to %q", before[ipv6Probe], active[ipv6Probe])
		}
		iface, err := net.InterfaceByName(darwinTUNDevice)
		if err != nil {
			return err
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return err
		}
		for _, addr := range addrs {
			prefix, err := netip.ParsePrefix(addr.String())
			if err == nil && prefix.Addr().Is6() && !prefix.Addr().IsLinkLocalUnicast() {
				t.Errorf("unexpected IPv6 tunnel address %s", prefix)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored := readRoutes(); !reflect.DeepEqual(before, restored) {
		t.Errorf("routes after stop: %v, before: %v", restored, before)
	}
	if clean, err := darwinTUNAbsent(t.Context()); err != nil || !clean {
		t.Fatalf("tunnel interface or routes remain: %v, %v", clean, err)
	}
}
