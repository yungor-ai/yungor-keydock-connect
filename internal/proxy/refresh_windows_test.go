package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestVerifiedCoreHandleProtectsTheImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.exe")
	image := []byte("synthetic image content")
	if err := os.WriteFile(path, image, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(image)
	file, err := openVerifiedCore(path, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if write, err := os.OpenFile(path, os.O_WRONLY, 0600); err == nil {
		write.Close()
		t.Fatal("a verified core could be opened for writing")
	}
	moved := path + ".old"
	if err := os.Rename(path, moved); err == nil {
		t.Fatal("a verified core could be renamed while protected")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, moved); err != nil {
		t.Fatalf("closing the handle did not release the image: %v", err)
	}
}

func TestWindowsRefreshKeepsOrRestoresTheActiveProxy(t *testing.T) {
	if os.Getenv("KEYDOCK_WINDOWS_PROXY_E2E") != "1" {
		t.Skip("requires a disposable Windows runner")
	}
	m := New(t.TempDir(), os.Getenv("KEYDOCK_TEST_CORE"))
	original, err := currentSettings()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Stop(); err != nil {
			t.Error(err)
		}
		if got, err := currentSettings(); err != nil || got != original {
			t.Errorf("original settings not restored: %v", err)
			_ = applySettings(original)
		}
	})
	initial := strings.ReplaceAll(peerSubscription(1), "MATCH,test-node", "MATCH,DIRECT")
	var body atomic.Value
	body.Store(initial)
	var allowed atomic.Bool
	allowed.Store(true)
	subscription := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, body.Load().(string))
	}))
	defer subscription.Close()

	refresh := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return m.refresh(ctx, subscription.URL, subscription.Client().Transport.(*http.Transport).TLSClientConfig)
	}
	running := func() *child {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.running
	}
	m.mu.Lock()
	err = m.startProfileLocked([]byte(initial))
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	first := running()
	before, err := currentSettings()
	if err != nil {
		t.Fatal(err)
	}
	body.Store(initial + "\n# no effective change\n")
	if err := refresh(); err != nil || running() != first {
		t.Fatalf("unchanged config should retain the process: %v", err)
	}
	allowed.Store(false)
	if err := refresh(); err == nil || running() != first {
		t.Fatal("refused download replaced the active process")
	}
	allowed.Store(true)
	body.Store("invalid subscription")
	if err := refresh(); err == nil || running() != first {
		t.Fatal("invalid subscription replaced the active process")
	}
	body.Store(strings.ReplaceAll(initial, "MATCH,DIRECT", "MATCH,REJECT"))
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	updated := running()
	if updated == nil || updated == first || updated.port != first.port {
		t.Fatal("changed subscription did not replace the worker on the owned port")
	}
	if got, err := currentSettings(); err != nil || got != before {
		t.Fatalf("refresh changed Windows proxy settings: %v", err)
	}
	// A worker that fails immediately after replacement must be rolled back,
	// using the last effective config and the same WinINet ownership.
	m.mu.Lock()
	err = m.replaceProfileLocked(context.Background(), updated, []byte(": invalid"))
	m.mu.Unlock()
	rolledBack := running()
	if err == nil || rolledBack == nil || rolledBack == updated || string(rolledBack.profile) != string(updated.profile) {
		t.Fatalf("failed replacement did not restore the previous profile: %v", err)
	}
	if got, err := currentSettings(); err != nil || got != before || m.ActiveMode() != ModeSystem {
		t.Fatalf("rollback changed Windows settings or mode: %v", err)
	}
}
