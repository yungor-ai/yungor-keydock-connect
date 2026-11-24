//go:build windows

package codex

import (
	"bytes"
	"testing"
)

func TestWindowsProtectionRoundTrip(t *testing.T) {
	secret := []byte("sample-only-dpapi-check")
	ciphertext, err := Protect(secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, secret) {
		t.Fatal("plaintext in encrypted blob")
	}
	plain, err := Unprotect(ciphertext)
	if err != nil || !bytes.Equal(secret, plain) {
		t.Fatal("DPAPI round trip failed", err)
	}
}
