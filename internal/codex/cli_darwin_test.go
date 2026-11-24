//go:build darwin

package codex

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Set KEYDOCK_CODEX_TEST_BINARY to an installed native Codex CLI to verify the
// generated config against that release. The macOS seatbelt explicitly denies
// non-loopback networking; the process also receives an isolated home and env.
func TestInstalledCodexWithLocalGateway(t *testing.T) {
	binary := os.Getenv("KEYDOCK_CODEX_TEST_BINARY")
	if binary == "" {
		t.Skip("set KEYDOCK_CODEX_TEST_BINARY to opt in")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("expected absolute native binary path")
	}
	dir := t.TempDir()
	const secret = "synthetic-fixture-credential"
	const reply = "fixture-gateway-ok"
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+secret {
			http.Error(w, "missing expected synthetic auth", 401)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		message := map[string]any{"type": "message", "id": "msg_test", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": reply, "annotations": []any{}}}}
		events := []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": "resp_test", "status": "in_progress", "output": []any{}}},
			{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "message", "id": "msg_test", "role": "assistant", "content": []any{}}},
			{"type": "response.output_text.delta", "item_id": "msg_test", "output_index": 0, "content_index": 0, "delta": reply},
			{"type": "response.output_item.done", "output_index": 0, "item": message},
			{"type": "response.completed", "response": map[string]any{"id": "resp_test", "status": "completed", "output": []any{message}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15}}},
		}
		for _, ev := range events {
			raw, _ := json.Marshal(ev)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev["type"], raw)
			w.(http.Flusher).Flush()
		}
	}))
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	root.Subject.CommonName = "KeyDock offline fixture CA"
	caDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, NotBefore: root.NotBefore, NotAfter: root.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, &leafKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	ca := filepath.Join(dir, "test-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "Synthetic Connect")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = --credential ] || exit 2\nprintf '%s\\n' "+secret+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	store := testStore(t)
	store.executable = func() (string, error) { return helper, nil }
	home := filepath.Join(dir, "home")
	if err := store.apply(home, filepath.Join(dir, "state"), server.URL+"/v1", "gpt-6.1-sol", secret); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	out := filepath.Join(dir, "result")
	profile := "(version 1) (allow default) (deny network-outbound) (allow network-outbound (remote ip \"localhost:*\")) (allow network-outbound (remote unix-socket))"
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", "-p", profile, binary, "exec", "--strict-config", "--skip-git-repo-check", "--ephemeral", "--ignore-rules", "--sandbox", "read-only", "-c", `web_search="disabled"`, "-o", out, "Return fixture-gateway-ok.")
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + dir, "CFFIXED_USER_HOME=" + dir, "CODEX_HOME=" + home, "PATH=/usr/bin:/bin", "TMPDIR=" + dir, "CODEX_CA_CERTIFICATE=" + ca, "SSL_CERT_FILE=" + ca, "HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "ALL_PROXY=http://127.0.0.1:1", "NO_PROXY=localhost,127.0.0.1,::1"}
	log, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated Codex failed: %v\n%s", err, strings.ReplaceAll(string(log), secret, "[synthetic]"))
	}
	if requests.Load() == 0 || strings.TrimSpace(read(t, out)) != reply {
		t.Fatalf("no authenticated, completed fixture response")
	}
}
