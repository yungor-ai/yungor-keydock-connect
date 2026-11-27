package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"gopkg.in/yaml.v3"
)

const windowsTLSMarker = "encrypted Windows TUN traffic"

type windowsHTTPSPeer struct {
	ssPort int
	stop   func()
	target *httptest.Server
	seen   <-chan string
}

// Each fixture destination is either forwarded to the trusted TLS origin or
// closed at the exit. Unrelated runner traffic is never forwarded to the origin.
func newWindowsHTTPSPeer(t *testing.T, core string, destinations map[string]bool) *windowsHTTPSPeer {
	t.Helper()
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, windowsTLSMarker)
	}))
	t.Cleanup(target.Close)
	seen := make(chan string, 8)
	exit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		forward, expected := destinations[r.Host]
		if !expected {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case seen <- r.Host:
		default:
			return
		}
		client, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer client.Close()
		if _, err = buffered.WriteString("HTTP/1.1 200 Connection established\r\n\r\n"); err != nil || buffered.Flush() != nil {
			return
		}
		if !forward {
			return
		}
		destination, err := net.DialTimeout("tcp4", target.Listener.Addr().String(), 2*time.Second)
		if err != nil {
			return
		}
		defer destination.Close()
		go func() {
			io.Copy(destination, buffered)
			destination.Close()
		}()
		io.Copy(client, destination)
	}))
	t.Cleanup(exit.Close)
	ssPort := reservePort(t)
	peer := fmt.Sprintf(`mode: rule
log-level: warning
listeners:
  - name: fixture-ss
    type: shadowsocks
    listen: 127.0.0.1
    port: %d
    cipher: 2022-blake3-aes-128-gcm
    password: "AAAAAAAAAAAAAAAAAAAAAA=="
    users: {test-device: "AQEBAQEBAQEBAQEBAQEBAQ=="}
proxies:
  - {name: fixture-exit, type: http, server: 127.0.0.1, port: %d}
rules:
  - MATCH,fixture-exit
`, ssPort, exit.Listener.Addr().(*net.TCPAddr).Port)
	stopPeer := runFixtureCore(t, core, []byte(peer), ssPort)
	return &windowsHTTPSPeer{ssPort, stopPeer, target, seen}
}

func (p *windowsHTTPSPeer) checkExit(t *testing.T, expected string) {
	t.Helper()
	select {
	case destination := <-p.seen:
		if destination != expected {
			t.Fatalf("SS2022 exit received %q, want %q", destination, expected)
		}
		t.Logf("SS2022 exit received %s", destination)
	case <-time.After(2 * time.Second):
		t.Fatalf("no request for %q reached the SS2022 exit", expected)
	}
}
func (p *windowsHTTPSPeer) request(authority string, proxyURL *url.URL, addresses ...string) error {
	transport := p.target.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	if proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	if len(addresses) > 0 {
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: 2 * time.Second}
			var err error
			for _, addr := range addresses {
				var conn net.Conn
				conn, err = dialer.DialContext(ctx, "tcp", addr)
				if err == nil {
					return conn, nil
				}
			}
			return nil, err
		}
	}
	transport.DisableKeepAlives = true
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 6 * time.Second}
	resp, err := client.Get("https://" + authority + "/")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(payload) != windowsTLSMarker {
		return fmt.Errorf("unexpected HTTPS response %s (%q)", resp.Status, payload)
	}
	return err
}

// Only this disposable runner's primary IPv4 DNS is replaced. Forward unrelated
// requests to its original upstream, and restore DHCP or static DNS on cleanup.
func installWindowsFixtureDNS(t *testing.T, core string, answers map[string]netip.Addr) {
	t.Helper()
	index, err := getBestInterface()
	if err != nil {
		t.Fatal(err)
	}
	query := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
$a = Get-NetAdapter -InterfaceIndex %d
$k = Get-Item ("HKLM:\SYSTEM\CurrentControlSet\Services\Tcpip\Parameters\Interfaces\" + ([guid]$a.InterfaceGuid).ToString('B'))
if ($k.GetValue('NameServer')) { 'static' } else { 'dhcp' }
(Get-DnsClientServerAddress -InterfaceIndex %d -AddressFamily IPv4).ServerAddresses`, index, index)
	output, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", query).CombinedOutput()
	if err != nil {
		t.Fatalf("read primary IPv4 DNS: %v: %s", err, output)
	}
	prior := strings.Fields(string(output))
	if len(prior) < 2 || prior[0] != "static" && prior[0] != "dhcp" {
		t.Fatalf("unexpected primary DNS snapshot %q", output)
	}
	for _, server := range prior[1:] {
		if ip, err := netip.ParseAddr(server); err != nil || !ip.Is4() || ip.IsLoopback() {
			t.Fatalf("need an upstream IPv4 resolver on the disposable runner, got %q", server)
		}
	}

	var dns strings.Builder
	dns.WriteString("hosts:\n")
	for host, ip := range answers {
		fmt.Fprintf(&dns, "  %s: %s\n", host, ip)
	}
	fmt.Fprintf(&dns, `dns:
  enable: true
  ipv6: false
  enhanced-mode: redir-host
  listen: 127.0.0.1:53
  nameserver: [%s]
`, strings.Join(prior[1:], ", "))
	runFixtureCore(t, core, []byte(dns.String()), 53)

	setDNS := func(verb string, args ...string) error {
		cmd := append([]string{"interface", "ipv4", verb, "dnsservers", fmt.Sprintf("name=%d", index)}, args...)
		if output, err := exec.Command("netsh.exe", cmd...).CombinedOutput(); err != nil {
			return fmt.Errorf("netsh IPv4 DNS %s: %w: %s", verb, err, output)
		}
		return nil
	}
	t.Cleanup(func() {
		if prior[0] == "dhcp" {
			if err := setDNS("set", "source=dhcp"); err != nil {
				t.Error(err)
			}
			return
		}
		if err := setDNS("set", "source=static", "address="+prior[1], "validate=no"); err != nil {
			t.Error(err)
			return
		}
		for i, address := range prior[2:] {
			if err := setDNS("add", "address="+address, fmt.Sprintf("index=%d", i+2), "validate=no"); err != nil {
				t.Error(err)
			}
		}
	})
	if err := setDNS("set", "source=static", "address=127.0.0.1", "validate=no"); err != nil {
		t.Fatal(err)
	}
}

// Model the suffix-specific resolver policy installed by a corporate VPN.
// Use a unique rule so cleanup can delete just this test's rule after a failure.
func installWindowsFixtureNRPT(t *testing.T, zone string) {
	t.Helper()
	run := func(script string) error {
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference = 'Stop'; "+script)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("configure fixture NRPT: %w: %s", err, output)
		}
		return nil
	}
	t.Cleanup(func() {
		err := run(fmt.Sprintf(`Get-DnsClientNrptRule | Where-Object { $_.Comment -eq '%s' } | ForEach-Object { Remove-DnsClientNrptRule -Name $_.Name -Force }`, zone))
		if err != nil {
			t.Error(err)
		}
	})
	if err := run(fmt.Sprintf(`Add-DnsClientNrptRule -Namespace '.%s' -NameServers '127.0.0.1' -Comment '%s'`, zone, zone)); err != nil {
		t.Fatal(err)
	}
}

// GetAddrInfoW is the default Windows resolver used by Go's TCP dialer.
func checkWindowsSystemAnswer(t *testing.T, host string, wrong netip.Addr, usingTUN bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	answers, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(answers) != 1 {
		t.Fatalf("Windows DNS for %s: %v, %v", host, answers, err)
	}
	ip := answers[0].Unmap()
	if usingTUN {
		if ip == wrong || !netip.MustParsePrefix("198.18.0.0/16").Contains(ip) {
			t.Fatalf("%s resolved to %v, want a KeyDock fake IPv4", host, ip)
		}
	} else if ip != wrong {
		t.Fatalf("%s resolved to %v, want the fixture DNS answer %v", host, ip, wrong)
	}
	t.Logf("Windows DNS resolved %s to %s", host, ip)
}

func TestWindowsTUNSystemDNSLifecycle(t *testing.T) {
	if os.Getenv("KEYDOCK_WINDOWS_TUN_E2E") != "1" {
		t.Skip("requires a disposable elevated Windows runner")
	}
	core := os.Getenv("KEYDOCK_TEST_CORE")
	if core == "" || !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("requires the packaged core and an elevated token")
	}

	// The TLS fixture trusts *.example.com. Fresh one-label names avoid the
	// Windows DNS cache and also verify the certificate against the real SNI.
	suffix := fmt.Sprintf("-%d.example.com", time.Now().UnixNano())
	system, legacy, current, after := "system"+suffix, "legacy"+suffix, "tun"+suffix, "after"+suffix
	wrong := netip.MustParseAddr("198.18.250.230")
	private := netip.MustParseAddr("10.74.63.52")
	zone := "office" + suffix
	beforeSplit, duringSplit, afterSplit := "before."+zone, "during."+zone, "after."+zone
	installWindowsFixtureDNS(t, core, map[string]netip.Addr{
		system: wrong, legacy: wrong, current: wrong, after: wrong,
		beforeSplit: private, duringSplit: private, afterSplit: private,
	})
	installWindowsFixtureNRPT(t, zone)
	checkWindowsSystemAnswer(t, beforeSplit, private, false)
	checkWindowsSystemAnswer(t, system, wrong, false)
	peer := newWindowsHTTPSPeer(t, core, map[string]bool{
		system + ":443": true, current + ":443": true, wrong.String() + ":443": false,
	})
	rules := "DOMAIN-SUFFIX,example.com,test-node\n  - IP-CIDR," + wrong.String() + "/32,test-node,no-resolve\n  - MATCH,DIRECT"
	source := []byte(strings.Replace(peerSubscription(peer.ssPort), "MATCH,test-node", rules, 1))
	port := reservePort(t)
	m := New(t.TempDir(), core)
	stop := func() {
		t.Helper()
		if err := m.Stop(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(stop)
	start := func(mode Mode, oldDNS bool) {
		t.Helper()
		config, err := m.windowsProfile(source, port, mode)
		if err != nil {
			t.Fatal(err)
		}
		if oldDNS {
			// Reproduce the v0.4.10 resolver policy, retaining the same Wintun
			// device, strict routing, sniffer, port and SS2022 subscription.
			var profile map[string]any
			if err := yaml.Unmarshal(config, &profile); err != nil {
				t.Fatal(err)
			}
			delete(profile, "dns")
			profile["tun"].(map[string]any)["dns-mode"] = "disabled"
			config, err = yaml.Marshal(profile)
			if err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(t.Context(), 75*time.Second)
		defer cancel()
		m.mu.Lock()
		err = m.startModeProfileLocked(ctx, config, port, mode)
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	start(ModeSystem, false)
	settings, err := currentSettings()
	if err != nil || settings.Flags&2 == 0 {
		t.Fatalf("read active WinINet proxy: %+v, %v", settings.Flags, err)
	}
	if err := peer.request(system, &url.URL{Scheme: "http", Host: settings.Server}); err != nil {
		t.Fatalf("system proxy HTTPS: %v", err)
	}
	peer.checkExit(t, system+":443")
	stop()

	start(ModeTUN, true)
	checkWindowsSystemAnswer(t, legacy, wrong, false)
	if err := peer.request(legacy, nil); err == nil {
		t.Fatal("legacy TUN unexpectedly completed HTTPS to the fixture's unusable address")
	}
	peer.checkExit(t, wrong.String()+":443")
	stop()

	start(ModeTUN, false)
	checkWindowsSystemAnswer(t, current, wrong, true)
	checkWindowsSystemAnswer(t, duringSplit, private, false)
	if err := peer.request(current, nil); err != nil {
		t.Fatalf("TUN HTTPS via Windows DNS: %v", err)
	}
	peer.checkExit(t, current+":443")
	stop()
	checkWindowsSystemAnswer(t, after, wrong, false)
	checkWindowsSystemAnswer(t, afterSplit, private, false)
}
