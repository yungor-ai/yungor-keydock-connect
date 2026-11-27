package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yungor-ai/yungor-keydock-connect/internal/diagnostics"
)

// Windows sing-tun rejects a supplied file descriptor before creating an
// adapter. Exercise the real core's startup error while its mixed port can open.
func TestWindowsTUNCoreStartupFailure(t *testing.T) {
	if os.Getenv("KEYDOCK_WINDOWS_TUN_E2E") != "1" {
		t.Skip("requires the disposable Windows TUN runner")
	}
	core := os.Getenv("KEYDOCK_TEST_CORE")
	if core == "" {
		t.Fatal("missing staged proxy core")
	}
	dir := t.TempDir()
	manager := New(dir, core)
	log := diagnostics.New(dir)
	manager.SetDiagnostics(log)
	t.Cleanup(func() {
		if err := manager.Stop(); err != nil {
			t.Error(err)
		}
	})
	before, err := getBestInterface()
	if err != nil {
		t.Fatal(err)
	}
	beforeProxy, err := currentSettings()
	if err != nil {
		t.Fatal(err)
	}
	port := reservePort(t)
	config, err := buildModeProfile([]byte(peerSubscription(1)), port, ModeTUN)
	if err != nil {
		t.Fatal(err)
	}
	config = bytes.Replace(config, []byte("\ntun:\n"), []byte("\ntun:\n    file-descriptor: -1\n"), 1)
	if !bytes.Contains(config, []byte("file-descriptor:")) {
		t.Fatal("fault was not injected")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	err = manager.startModeProfileLocked(ctx, config, port, ModeTUN)
	var failure interface{ DiagnosticClass() string }
	if !errors.As(err, &failure) || failure.DiagnosticClass() != "tun_core_adapter" {
		t.Fatalf("expected an adapter startup error from the bundled core, got %v", err)
	}
	if mode := manager.ActiveMode(); mode != "" {
		t.Fatalf("TUN marked active after core initialization failed: %s", mode)
	}
	if after, err := getBestInterface(); err != nil || after != before {
		t.Fatalf("original default route was not preserved: %v", err)
	}
	if after, err := currentSettings(); err != nil || after != beforeProxy {
		t.Fatalf("system proxy settings changed after a TUN startup failure: %v", err)
	}
	path := filepath.Join(t.TempDir(), "export.json")
	if err := diagnostics.New(dir).Export(path, diagnostics.State{}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report struct{ Events []diagnostics.Event }
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	for _, event := range report.Events {
		if event.Outcome == "failed" && event.ErrorClass == "tun_core_adapter" {
			return
		}
	}
	t.Fatal("export did not preserve the core's sanitized startup failure")
}
