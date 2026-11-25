package proxy

import (
	"context"
	"os"
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
}
