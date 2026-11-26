package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsTUNRoutesAndWinINetRestore(t *testing.T) {
	if os.Getenv("KEYDOCK_WINDOWS_TUN_E2E") != "1" {
		t.Skip("requires a disposable elevated Windows runner")
	}
	core := os.Getenv("KEYDOCK_TEST_CORE")
	if core == "" {
		t.Fatal("missing staged proxy core")
	}
	m := New(t.TempDir(), core)
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("the disposable TUN runner needs an elevated token")
	}
	settingsBefore, err := currentSettings()
	if err != nil {
		t.Fatal(err)
	}
	before, err := getBestInterface()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Stop(); err != nil {
			t.Error(err)
		}
	})
	// The real adapter captures routes, but the isolated profile keeps all
	// traffic direct so a disposable runner can retain its connection.
	body := strings.Replace(fixture, "type: url-test", "type: select", 1)
	body = strings.Replace(body, "MATCH,Proxy", "MATCH,DIRECT", 1)
	port, err := loopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	config, err := buildModeProfile([]byte(body), port, ModeTUN)
	if err != nil {
		t.Fatal(err)
	}
	start := func() uint64 {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		m.mu.Lock()
		err := m.startModeProfileLocked(ctx, config, port, ModeTUN)
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		luid, index, err := findTUNAdapter()
		if err != nil || luid == 0 || index == before || m.ActiveMode() != ModeTUN {
			t.Fatalf("the selected mode did not create a new adapter: %v", err)
		}
		if selected, err := getBestInterface(); err != nil || selected != index {
			t.Fatalf("Windows did not select the TUN route: %v", err)
		}
		row := windows.MibIpInterfaceRow{Family: windows.AF_INET, InterfaceLuid: luid}
		if err := windows.GetIpInterfaceEntry(&row); err != nil || row.NlMtu != 1500 {
			t.Fatalf("Windows TUN MTU = %d, want 1500: %v", row.NlMtu, err)
		}
		if settings, err := currentSettings(); err != nil || settings != settingsBefore {
			t.Fatal("TUN changed Windows system proxy settings")
		}
		return luid
	}
	checkRestored := func(luid uint64) {
		t.Helper()
		if remaining, err := tunRoutes(luid); err != nil || len(remaining) != 0 || m.ActiveMode() != "" {
			t.Fatalf("TUN mode or its default routes survived cleanup: %v", err)
		}
		if selected, err := getBestInterface(); err != nil || selected != before {
			t.Fatalf("Windows did not resume its original route: %v", err)
		}
		if settings, err := currentSettings(); err != nil || settings != settingsBefore {
			t.Fatal("Windows system proxy settings differ after stopping TUN")
		}
	}
	luid := start()
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	checkRestored(luid)
	luid = start()
	m.mu.Lock()
	err = m.running.cmd.Process.Kill()
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for m.Active() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if m.Active() {
		t.Fatal("crashed TUN worker was not detected")
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	checkRestored(luid)
	// A worker terminated without its graceful cleanup must also allow the user
	// to start TUN again after its traffic routes have been restored.
	luid = start()
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	checkRestored(luid)
	t.Run("IPv4OnlyProxyPeer", func(t *testing.T) {
		verifyWindowsTUNIPv4OnlyPeer(t, core)
	})
}

func verifyWindowsTUNIPv4OnlyPeer(t *testing.T, core string) {
	t.Helper()
	const hostname = "dual-stack.example.com"
	const ipv4 = "203.0.113.41:443"
	const ipv6 = "[2001:db8::41]:443"
	const marker = "encrypted Windows TUN traffic"
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, marker)
	}))
	t.Cleanup(target.Close)
	seen := make(chan string, 8)
	// The fixture exit can resolve the domain or reach its IPv4 address. It
	// observes raw IPv6 requests before closing them like an IPv4-only exit.
	exit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Host != hostname+":443" && r.Host != ipv4 && r.Host != ipv6 {
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
		if r.Host == ipv6 {
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
	port := reservePort(t)
	body := []byte(peerSubscription(ssPort))
	m := New(t.TempDir(), core)
	stop := func() {
		t.Helper()
		if err := m.Stop(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(stop)
	start := func(mode Mode, tun *tunSettings) {
		t.Helper()
		config, err := buildRoutingProfileWithTUN(body, port, false, mode, tun)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		m.mu.Lock()
		err = m.startModeProfileLocked(ctx, config, port, mode)
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	checkExit := func(expected string) {
		t.Helper()
		select {
		case destination := <-seen:
			if destination != expected {
				t.Fatalf("SS2022 exit received %q, want %q", destination, expected)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no request for %q reached the SS2022 exit", expected)
		}
	}
	request := func(authority string, proxyURL *url.URL, addresses ...string) error {
		transport := target.Client().Transport.(*http.Transport).Clone()
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
		if resp.StatusCode != http.StatusOK || string(payload) != marker {
			return fmt.Errorf("unexpected HTTPS response %s (%q)", resp.Status, payload)
		}
		return err
	}
	hasTUNIPv6Route := func() bool {
		t.Helper()
		luid, _, err := findTUNAdapter()
		if err != nil || luid == 0 {
			t.Fatalf("TUN adapter missing: %v", err)
		}
		routes, err := tunRoutes(luid)
		if err != nil {
			t.Fatal(err)
		}
		for _, route := range routes {
			if route.DestinationPrefix.Prefix.Family == windows.AF_INET6 {
				return true
			}
		}
		return false
	}
	localProxy := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	start(ModeSystem, nil)
	if err := request(hostname, localProxy); err != nil {
		t.Fatalf("system proxy could not reach the IPv4-only SS2022 exit: %v", err)
	}
	checkExit(hostname + ":443")
	stop()

	legacy := makeTUNSettings(tunDeviceName)
	legacy.MTU = windowsTUNMTU
	legacy.Inet6Address = &[]string{"fdfe:dcba:9876::1/126"}
	start(ModeTUN, legacy)
	legacyIPv6 := func() error {
		if hasTUNIPv6Route() {
			return request(hostname, nil, ipv6)
		}
		// Mihomo suppresses the legacy IPv6 TUN route when the runner has
		// no global-unicast IPv6. Exercise the same literal SS2022 target
		// through its loopback listener on such a runner.
		t.Log("runner has no legacy IPv6 TUN route; checking the raw IPv6 target through the mixed listener")
		return request(ipv6, localProxy)
	}
	if err := legacyIPv6(); err == nil {
		t.Fatal("the legacy TUN forwarded a raw IPv6 destination successfully")
	}
	checkExit(ipv6)
	stop()

	start(ModeTUN, nil)
	if hasTUNIPv6Route() {
		t.Fatal("IPv4-only TUN installed an IPv6 default route")
	}
	if err := request(hostname, nil, ipv6); err == nil {
		t.Fatal("IPv4-only TUN allowed the raw IPv6 connection")
	}
	select {
	case address := <-seen:
		t.Fatalf("IPv4-only TUN forwarded unexpected destination %q", address)
	default:
	}
	if err := request(hostname, nil, ipv6, ipv4); err != nil {
		t.Fatalf("TUN could not fall back to IPv4 through the SS2022 exit: %v", err)
	}
	checkExit(ipv4)
	stopPeer()
	if err := request(hostname, nil, ipv4); err == nil {
		t.Fatal("TUN traffic bypassed the stopped SS2022 peer")
	}
}
