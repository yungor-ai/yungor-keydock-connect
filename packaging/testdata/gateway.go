// Local TLS fixture for the disposable Windows installer acceptance test.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	if len(os.Args) != 3 {
		panic("expected fixture directory and loopback SS server port")
	}
	dir := os.Args[1]
	ssPort, err := strconv.Atoi(os.Args[2])
	check(err)
	if ssPort < 1 || ssPort > 65535 {
		panic("invalid fixture SS port")
	}
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(err)
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	check(err)
	now := time.Now()
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Disposable KeyDock test CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	ca, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	check(err)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, NotBefore: root.NotBefore, NotAfter: root.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	crt, err := x509.CreateCertificate(rand.Reader, leaf, root, &leafKey.PublicKey, rootKey)
	check(err)
	check(os.WriteFile(filepath.Join(dir, "test-ca.cer"), ca, 0600))
	listen, err := net.Listen("tcp4", "127.0.0.1:0")
	check(err)
	origin := "https://" + listen.Addr().String()
	mux := http.NewServeMux()
	clockSkewFailure := filepath.Join(dir, "proxy-clock-skew-failure")
	mux.HandleFunc("GET /api/connect/v1/update", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("platform") != "windows-x64" || r.URL.Query().Get("version") == "" || r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected update check", http.StatusBadRequest)
			return
		}
		digest := strings.Repeat("a", 64)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"update_available": true, "latest_version": "99.0.0", "sha256": digest,
			"download_path": "/api/software/keydock-connect-windows-x64/download?digest=" + digest,
		})
	})
	mux.HandleFunc("GET /api/software/keydock-connect-windows-x64/download", func(w http.ResponseWriter, r *http.Request) {
		check(os.WriteFile(filepath.Join(dir, "unexpected-update-download"), nil, 0600))
		http.Error(w, "fixture does not serve update binaries", http.StatusNotFound)
	})
	mux.HandleFunc("POST /api/connect/v1/device", func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(filepath.Join(dir, "device-revoked")); err == nil || r.Header.Get("Authorization") != "Bearer fixture-device-key" {
			http.Error(w, "denied", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := os.Stat(clockSkewFailure); err == nil {
			w.Header().Set("Date", time.Now().UTC().Add(45*time.Second).Format(http.TimeFormat))
		}
		var subscription string
		if _, err := os.Stat(filepath.Join(dir, "subscription-enabled")); err == nil {
			subscription = origin + "/subscription"
		}
		json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "device_name": "演示工作站", "model": "synthetic-model", "api_base_url": origin + "/v1", "api_key": "synthetic-ci-helper-key", "subscription_url": subscription})
	})
	mux.HandleFunc("GET /subscription", func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(clockSkewFailure); err == nil {
			http.Error(w, "fixture proxy unavailable", http.StatusServiceUnavailable)
			return
		}
		// Both fixture keys are synthetic. This server is bound to loopback.
		fmt.Fprintf(w, "proxies:\n  - {name: test-node, type: ss, server: 127.0.0.1, port: %d, cipher: 2022-blake3-aes-128-gcm, password: \"AAAAAAAAAAAAAAAAAAAAAA==:AQEBAQEBAQEBAQEBAQEBAQ==\"}\nrules: [\"MATCH,test-node\"]\n", ssPort)
	})
	mux.HandleFunc("GET /check-proxy", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "KeyDock fixture reached")
	})
	mux.HandleFunc("POST /v1/responses", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-ci-helper-key" {
			http.Error(w, "denied", 401)
			return
		}
		var req struct {
			Model  string
			Stream bool
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Model != "synthetic-model" || !req.Stream {
			http.Error(w, "unexpected request", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}]}}\n\n")
	})
	ready := filepath.Join(dir, "fixture-origin")
	check(os.WriteFile(ready+".tmp", []byte(origin), 0600))
	check(os.Rename(ready+".tmp", ready))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 4 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{crt, ca}, PrivateKey: leafKey}}}}
	check(srv.ServeTLS(listen, "", ""))
}
