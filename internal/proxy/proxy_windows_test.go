package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
