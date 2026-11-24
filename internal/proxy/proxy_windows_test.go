package proxy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWindowsProxyCoreLifecycle(t *testing.T) {
	if os.Getenv("KEYDOCK_WINDOWS_PROXY_E2E") != "1" {
		t.Skip("requires a disposable Windows runner")
	}
	core, err := filepath.Abs(os.Getenv("KEYDOCK_TEST_CORE"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(t.TempDir(), core)
	original, err := currentSettings()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Stop(); err != nil {
			t.Error(err)
		}
		now, err := currentSettings()
		if err != nil || now != original {
			t.Errorf("Windows settings were not restored")
			_ = applySettings(original)
		}
	})
	body := strings.ReplaceAll(fixture, "MATCH,Proxy", "MATCH,DIRECT")
	// No active health-check connects to the test-only SS2022 node.
	body = strings.Replace(body, "type: url-test", "type: select", 1)
	start := func() {
		t.Helper()
		m.mu.Lock()
		err := m.startProfileLocked([]byte(body))
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	start()
	now, err := currentSettings()
	if err != nil || !m.Active() || !strings.HasPrefix(now.Server, "127.0.0.1:") {
		t.Fatal("system proxy did not activate")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "real proxy online") }))
	defer srv.Close()
	u, err := url.Parse("http://" + now.Server)
	if err != nil {
		t.Fatal(err)
	}
	tr := &http.Transport{Proxy: http.ProxyURL(u)}
	defer tr.CloseIdleConnections()
	c := http.Client{Transport: tr, Timeout: 8 * time.Second}
	res, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil || string(b) != "real proxy online" {
		t.Fatal("request did not traverse core")
	}
	if err = m.Stop(); err != nil {
		t.Fatal(err)
	}
	if current, _ := currentSettings(); current != original {
		t.Fatal("normal stop did not restore settings")
	}
	start()
	m.mu.Lock()
	err = m.running.cmd.Process.Kill()
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(8 * time.Second)
	for m.Active() && time.Now().Before(until) {
		time.Sleep(50 * time.Millisecond)
	}
	if m.Active() {
		t.Fatal("crashed process remains active")
	}
	if current, _ := currentSettings(); current != original {
		t.Fatal("crash cleanup did not restore settings")
	}
}

func TestWindowsProbeUsesSubscriptionWithoutChangingWindowsSettings(t *testing.T) {
	if os.Getenv("KEYDOCK_WINDOWS_PROXY_E2E") != "1" {
		t.Skip("requires a disposable Windows runner")
	}
	core := os.Getenv("KEYDOCK_TEST_CORE")
	port, stop := fixturePeer(t, core)
	var allow atomic.Bool
	allow.Store(true)
	var downloads, checks atomic.Int32
	// The delivered subscription sends all normal traffic direct. Only a test
	// that rebuilds its own SS-only route can detect the stopped peer below.
	body := strings.ReplaceAll(peerSubscription(port), "MATCH,test-node", "MATCH,DIRECT")
	endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("unexpected bearer on network diagnostic")
		}
		switch r.URL.Path {
		case "/sub":
			downloads.Add(1)
			if !allow.Load() {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			io.WriteString(w, body)
		case "/check":
			checks.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer endpoint.Close()
	cfg := endpoint.Client().Transport.(*http.Transport).TLSClientConfig
	m := New(t.TempDir(), core)
	orig, err := currentSettings()
	if err != nil {
		t.Fatal(err)
	}
	// Keep the user's actual proxy running throughout the independent check.
	m.mu.Lock()
	err = m.startProfileLocked([]byte(body))
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Stop(); err != nil {
			t.Error(err)
		}
		if now, _ := currentSettings(); now != orig {
			t.Error("original settings lost")
			_ = applySettings(orig)
		}
	})
	prior, err := currentSettings()
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context) error {
		return m.testConnection(ctx, endpoint.URL+"/sub", endpoint.URL+"/check", cfg)
	}
	if err := check(context.Background()); err != nil {
		t.Fatal(err)
	}
	allow.Store(false)
	if err := check(context.Background()); err == nil {
		t.Error("revoked subscription reported healthy")
	}
	allow.Store(true)
	stop()
	if err := check(context.Background()); err == nil {
		t.Error("failed SS2022 connection fell back to direct")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := check(ctx); err == nil {
		t.Error("cancelled probe succeeded")
	}
	if downloads.Load() != 3 || checks.Load() != 1 {
		t.Errorf("downloads %d, probes %d", downloads.Load(), checks.Load())
	}
	if now, err := currentSettings(); err != nil || now != prior || !m.Active() {
		t.Fatal("diagnosis interrupted the user's running proxy")
	}
	entries, err := filepath.Glob(filepath.Join(m.stateDir, "proxy-runtime-*"))
	if err != nil || len(entries) != 1 {
		t.Fatal("diagnostic process state not cleaned")
	}
}
