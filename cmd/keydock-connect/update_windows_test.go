//go:build windows

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLockWindowsInstallerKeepsVerifiedPathStable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keydock-update")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "setup.exe")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	open, err := lockWindowsInstaller(path)
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()
	got, err := io.ReadAll(open)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("locked installer was not readable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, path, "-test.run=^$").CombinedOutput(); err != nil {
		t.Fatalf("Windows could not load the locked executable: %v\n%s", err, output)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err == nil {
		t.Fatal("installer could be overwritten while verified")
	}
	renamed := filepath.Join(dir, "moved.exe")
	if err := os.Rename(path, renamed); err == nil {
		t.Fatal("installer could be moved while verified")
	}
	if err := os.Rename(dir, dir+"-moved"); err == nil {
		t.Fatal("installer directory could be moved while verified")
	}
	if err := open.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, renamed); err != nil {
		t.Fatalf("installer was still locked after close: %v", err)
	}
}
