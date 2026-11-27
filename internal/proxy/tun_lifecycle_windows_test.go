package proxy

import (
	"context"
	"errors"
	"net"
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
		ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
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
	// A real, fully configured adapter alone must not satisfy startup for a
	// child that has yet to report completing its own TUN initialization.
	unconfirmed := &child{}
	if step, err := unconfirmed.pendingTUNStep(); step != nil || err != nil {
		t.Fatalf("active TUN native checks failed: %v, %v", step, err)
	}
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 400*time.Millisecond)
	err = m.awaitTUN(probeCtx, unconfirmed)
	cancelProbe()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("accepted native routes without core readiness: %v", err)
	}
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
	peer := newWindowsHTTPSPeer(t, core, map[string]bool{hostname + ":443": true, ipv4: true, ipv6: false})
	port := reservePort(t)
	body := []byte(peerSubscription(peer.ssPort))
	m := New(t.TempDir(), core)
	stop := func() {
		t.Helper()
		if err := m.Stop(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(stop)
	start := func(mode Mode) {
		t.Helper()
		config, err := buildModeProfile(body, port, mode)
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
	start(ModeSystem)
	if err := peer.request(hostname, localProxy); err != nil {
		t.Fatalf("system proxy could not reach the IPv4-only SS2022 exit: %v", err)
	}
	peer.checkExit(t, hostname+":443")
	// Exercise the old form of the SS2022 destination on the same ready
	// proxy. Windows CI has no native global IPv6 route for the legacy TUN.
	if err := peer.request(ipv6, localProxy); err == nil {
		t.Fatal("the IPv4-only SS2022 exit accepted a raw IPv6 destination")
	}
	peer.checkExit(t, ipv6)
	stop()

	start(ModeTUN)
	if hasTUNIPv6Route() {
		t.Fatal("IPv4-only TUN installed an IPv6 default route")
	}
	if err := peer.request(hostname, nil, ipv6); err == nil {
		t.Fatal("IPv4-only TUN allowed the raw IPv6 connection")
	}
	select {
	case address := <-peer.seen:
		t.Fatalf("IPv4-only TUN forwarded unexpected destination %q", address)
	default:
	}
	// The worker can publish routes before its packet handler accepts requests.
	// Allow that startup transition, while failing any rejected request that
	// actually made it to the exit instead of hiding a destination error.
	deadline := time.Now().Add(4 * time.Second)
	for err := peer.request(hostname, nil, ipv6, ipv4); err != nil; err = peer.request(hostname, nil, ipv6, ipv4) {
		select {
		case address := <-peer.seen:
			t.Fatalf("TUN sent %q to the IPv4-only exit, but HTTPS failed: %v", address, err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("TUN could not fall back to IPv4 through the SS2022 exit: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	peer.checkExit(t, ipv4)
	peer.stop()
	if err := peer.request(hostname, nil, ipv4); err == nil {
		t.Fatal("TUN traffic bypassed the stopped SS2022 peer")
	}
}
