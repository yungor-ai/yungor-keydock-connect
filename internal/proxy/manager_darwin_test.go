//go:build darwin && arm64

package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if worker, err := RunWorker(os.Args[1:]); worker {
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 5 && os.Args[1] == "--proxy-parent-fixture" {
		port, _ := strconv.Atoi(os.Args[4])
		m := New(os.Args[3], os.Args[2])
		m.mu.Lock()
		// This fixture only waits for a local listener. No SS peer is contacted.
		profile := localProfile{port: port, body: []byte(peerSubscription(1))}
		err := m.startLocalProfileLocked(context.Background(), profile)
		m.mu.Unlock()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stdout, m.ProxyAddress())
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = m.Stop()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestDarwinLocalPortCollision(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	m := New(t.TempDir(), "unused")
	if err := m.startLocal(context.Background(), "", nil, listener.Addr().(*net.TCPAddr).Port); err == nil {
		t.Fatal("started on a port owned by a different process")
	}
	if m.Active() || m.ProxyAddress() != "" {
		t.Fatal("published an address without a managed listener")
	}
	if err := m.CheckMode(ModeTUN); err != nil {
		t.Fatalf("TUN mode unavailable: %v", err)
	}
	if err := m.CheckMode(ModeSystem); err != nil {
		t.Fatalf("system proxy mode unavailable: %v", err)
	}
	if err := m.CheckMode(ModeLocal); err != nil {
		t.Fatalf("local mode unavailable: %v", err)
	}
}

func TestDarwinRetainsUnconfirmedTUNCleanup(t *testing.T) {
	for _, initiated := range []string{"stop", "worker exit"} {
		t.Run(initiated, func(t *testing.T) {
			fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
			if err != nil {
				t.Fatal(err)
			}
			file := os.NewFile(uintptr(fds[0]), "cleanup-fixture")
			defer file.Close()
			defer unix.Close(fds[1])
			raw, err := net.FileConn(file)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			failure := errors.New("owned routes remain")
			done := make(chan struct{})
			p := &child{mode: ModeTUN, done: done, tun: &darwinTUNLease{
				conn: raw.(*net.UnixConn), done: done, err: failure,
			}}
			m := New(t.TempDir(), "unused")
			m.mu.Lock()
			m.trackDarwinChild(p)
			close(done)
			if initiated == "stop" {
				err = m.stopDarwinLocked()
			}
			m.mu.Unlock()
			if initiated == "stop" && !errors.Is(err, failure) {
				t.Fatalf("stop returned %v, want %v", err, failure)
			}

			for deadline := time.Now().Add(time.Second); m.Active() && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.running != nil || m.Active() || m.pendingTUN != p {
				t.Fatal("finished TUN must retain its pending cleanup after the worker is cleared")
			}
			if err := m.recheckDarwinTUNCleanupLocked(func() error { return failure }); !errors.Is(err, failure) {
				t.Fatalf("recheck returned %v, want %v", err, failure)
			}
			if m.pendingTUN != p {
				t.Fatal("discarded pending cleanup before owned routes disappeared")
			}
			if err := m.recheckDarwinTUNCleanupLocked(func() error { return nil }); err != nil || m.pendingTUN != nil {
				t.Fatalf("pending cleanup survived confirmed route removal: %v", err)
			}
		})
	}
}

func TestDarwinLocalProxyLifecycle(t *testing.T) {
	core := os.Getenv("KEYDOCK_TEST_CORE")
	if core == "" {
		t.Skip("set KEYDOCK_TEST_CORE to the packaged native proxy core")
	}
	core, err := filepath.Abs(core)
	if err != nil {
		t.Fatal(err)
	}
	ssPort, stopPeer := fixturePeer(t, core)
	original := peerSubscription(ssPort)
	var subscription atomic.Value
	subscription.Store(original)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sub" {
			fmt.Fprint(w, subscription.Load().(string))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	tlsConfig := server.Client().Transport.(*http.Transport).TLSClientConfig
	m := New(t.TempDir(), core)
	t.Cleanup(func() { _ = m.Stop() })

	if err := m.testConnection(ctx, server.URL+"/sub", server.URL+"/status", tlsConfig); err != nil {
		t.Fatalf("isolated SS2022 check failed: %v", err)
	}
	if m.Active() || m.ProxyAddress() != "" {
		t.Fatal("temporary diagnostic left a proxy active")
	}
	port, err := loopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.startLocal(ctx, server.URL+"/sub", tlsConfig, port); err != nil {
		t.Fatalf("start: %v", err)
	}
	address := fmt.Sprintf("127.0.0.1:%d", port)
	check := func() {
		t.Helper()
		if m.ActiveMode() != ModeLocal || m.ProxyAddress() != address {
			t.Fatalf("active locally at %q (%s), want %s (%s)", m.ProxyAddress(), m.ActiveMode(), address, ModeLocal)
		}
		if err := m.checkHTTPS(ctx, port, server.URL+"/status", tlsConfig); err != nil {
			t.Fatal(err)
		}
	}
	check()
	previous := m.active.Load()
	subscription.Store("not a KeyDock subscription")
	if err := m.refresh(ctx, server.URL+"/sub", tlsConfig); err == nil {
		t.Fatal("accepted an invalid refresh")
	}
	if m.active.Load() != previous {
		t.Fatal("discarded the prior worker after an invalid refresh")
	}
	check()
	subscription.Store(original[:len(original)-len("  - MATCH,test-node\n")] + "  - DOMAIN,example.invalid,REJECT\n  - MATCH,test-node\n")
	if err := m.refresh(ctx, server.URL+"/sub", tlsConfig); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if m.active.Load() == previous {
		t.Fatal("did not reload the changed subscription")
	}
	check()
	stopPeer()
	if err := m.testConnection(ctx, server.URL+"/sub", server.URL+"/status", tlsConfig); err == nil {
		t.Fatal("temporary diagnostic succeeded without an SS peer")
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	if m.Active() || m.ProxyAddress() != "" {
		t.Fatal("published a stopped listener")
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatalf("stopping the local proxy did not release the listener: %v", err)
	}
	listener.Close()
}

func TestDarwinProxyStopsWhenItsAppIsKilled(t *testing.T) {
	core := os.Getenv("KEYDOCK_TEST_CORE")
	if core == "" {
		t.Skip("set KEYDOCK_TEST_CORE to the packaged native proxy core")
	}
	core, err := filepath.Abs(core)
	if err != nil {
		t.Fatal(err)
	}
	port, err := loopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	app := exec.CommandContext(ctx, executable, "--proxy-parent-fixture", core, stateDir, strconv.Itoa(port))
	stdin, err := app.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := app.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	app.Stderr = os.Stderr
	if err = app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Wait() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != localAddress(port) {
		t.Fatalf("fixture app did not report the expected local address: %q, %v", scanner.Text(), scanner.Err())
	}
	if err = app.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	awaitDarwinProxyCleanup(t, stateDir, port)
}

func TestDarwinProxyStopsWhenItsSupervisorIsKilled(t *testing.T) {
	core := os.Getenv("KEYDOCK_TEST_CORE")
	if core == "" {
		t.Skip("set KEYDOCK_TEST_CORE to the packaged native proxy core")
	}
	port, err := loopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	m := New(t.TempDir(), core)
	t.Cleanup(func() { _ = m.Stop() })
	profile := localProfile{port: port, body: []byte(peerSubscription(1))}
	m.mu.Lock()
	err = m.startLocalProfileLocked(context.Background(), profile)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	p := m.active.Load()
	if p == nil || m.ProxyAddress() != localAddress(port) {
		t.Fatal("fixture did not start the managed proxy")
	}
	if err = p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	awaitDarwinProxyCleanup(t, m.stateDir, port)
	for deadline := time.Now().Add(time.Second); m.Active() && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if m.Active() {
		t.Fatal("managed proxy state survived the forced supervisor exit")
	}
	m.mu.Lock()
	err = m.startLocalProfileLocked(context.Background(), profile)
	m.mu.Unlock()
	if err != nil {
		t.Fatalf("restart after forced supervisor exit: %v", err)
	}
}

func awaitDarwinProxyCleanup(t *testing.T, stateDir string, port int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(stateDir)
		listener, err := net.Listen("tcp4", localAddress(port))
		if err == nil {
			listener.Close()
			if len(entries) == 0 {
				return
			}
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatal("local listener or its runtime directory survived an abrupt exit")
}
