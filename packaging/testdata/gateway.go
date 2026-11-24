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
	"time"
)

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	if len(os.Args) != 2 {
		panic("expected fixture output directory")
	}
	dir := os.Args[1]
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
	mux.HandleFunc("POST /api/connect/v1/device", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-device-key" {
			http.Error(w, "denied", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "device_name": "Windows smoke test", "model": "synthetic-model", "api_base_url": origin + "/v1", "api_key": "synthetic-ci-helper-key", "subscription_url": ""})
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
