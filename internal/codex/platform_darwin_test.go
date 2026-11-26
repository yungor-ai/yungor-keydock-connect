//go:build darwin && arm64 && cgo

package codex

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestMacKeychainCredentialLifecycle(t *testing.T) {
	keychain := newFixtureKeychain(t)
	home, dir := t.TempDir(), t.TempDir()
	store := system(dir)
	store.seal, store.open = keychain.protect(dir), keychain.unprotect(dir)
	const secret = "synthetic-macos-device-credential"
	if _, err := store.open([]byte("unreadable ciphertext")); err == nil {
		t.Fatal("credential lookup should fail before enrollment")
	}
	if err := store.apply(home, dir, gateway, "test-model", secret); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, stateName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(secret)) {
		t.Fatal("state file contains the credential in plaintext")
	}
	otherDir := t.TempDir()
	if _, err := keychain.protect(otherDir)([]byte("independent enrollment")); err != nil {
		t.Fatal(err)
	}
	if _, err := keychain.unprotect(otherDir)(data); err == nil {
		t.Fatal("another state directory decrypted this credential")
	}
	tampered := bytes.Clone(data)
	tampered[len(tampered)-1] ^= 1
	if _, err := store.open(tampered); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestMacKeychainCredentialChild$")
	cmd.Env = append(os.Environ(), "KEYDOCK_TEST_KEYCHAIN="+string(keychain), "KEYDOCK_TEST_STATE="+dir)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("credential subprocess failed: %v", err)
	}
	if !bytes.Equal(output, []byte(secret+"\n")) {
		t.Fatal("credential subprocess did not return the fixture credential")
	}
	if err := store.restore(home, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := store.credential(dir); err == nil {
		t.Fatal("credential remained after restore")
	}
}

func TestMacKeychainCredentialChild(t *testing.T) {
	keychain := darwinKeychain(os.Getenv("KEYDOCK_TEST_KEYCHAIN"))
	dir := os.Getenv("KEYDOCK_TEST_STATE")
	if keychain == "" || dir == "" {
		t.Skip("credential subprocess only")
	}
	store := system(dir)
	store.seal, store.open = keychain.protect(dir), keychain.unprotect(dir)
	key, err := store.credential(dir)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, key)
	os.Exit(0)
}

func newFixtureKeychain(t *testing.T) darwinKeychain {
	t.Helper()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "keychain-fixture")
	path := filepath.Join(dir, "keydock-tests.keychain")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/clang", "-Wno-deprecated-declarations", "-framework", "Security", "-framework", "CoreFoundation", "testdata/keychain-fixture.c", "-o", fixture)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build keychain fixture: %v\n%s", err, out)
	}
	if out, err := exec.CommandContext(ctx, fixture, "create", path).CombinedOutput(); err != nil {
		t.Fatalf("create isolated keychain: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, fixture, "delete", path).CombinedOutput(); err != nil {
			t.Errorf("delete isolated keychain: %v\n%s", err, out)
		}
	})
	return darwinKeychain(path)
}
