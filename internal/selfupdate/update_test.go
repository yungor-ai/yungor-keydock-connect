package selfupdate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSoftwareURLUsesTheConnectBase(t *testing.T) {
	for _, tc := range []struct{ server, want string }{
		{" https://keydock.example.com:8443/ ", "https://keydock.example.com:8443/software"},
		{"https://[2001:db8::1]:8443/edge/", "https://[2001:db8::1]:8443/edge/software"},
		{"http://keydock.example.com", ""},
		{"https://secret@keydock.example.com", ""},
		{"https://keydock.example.com/?token=secret", ""},
		{"https://keydock.example.com/#other", ""},
	} {
		url, err := SoftwareURL(tc.server)
		if url != tc.want || (err != nil) != (tc.want == "") {
			t.Errorf("SoftwareURL(%q) = %q, %v; want %q", tc.server, url, err, tc.want)
		}
	}
}

type testTransport func(*http.Request) (*http.Response, error)

func (transport testTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestApprovedUpdateRequiresMatchingPublisherAndPackage(t *testing.T) {
	for _, tc := range []struct{ name, platform, problem string }{
		{"Windows update", "windows/amd64", ""},
		{"macOS update", "darwin/arm64", ""},
		{"publisher mismatch", "windows/amd64", "wrong publisher digest"},
		{"download mismatch", "darwin/arm64", "wrong package bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const version = "0.9.10"
			const payload = "verified-install"
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
			target := platforms[tc.platform]
			route := "/api/software/keydock-connect-" + target.id + "/download"
			downloadPath := route + "?digest=" + digest
			var downloads atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("a public update request supplied credentials")
				}
				switch r.URL.Path {
				case "/edge/api/connect/v1/update":
					if r.URL.Query().Get("platform") != target.id || r.URL.Query().Get("version") != "0.9.9" {
						t.Errorf("unexpected update query: %s", r.URL.RawQuery)
					}
					fmt.Fprintf(w, `{"update_available":true,"latest_version":%q,"sha256":%q,"download_path":%q}`, version, digest, downloadPath)
				case "/edge" + route:
					downloads.Add(1)
					if r.URL.Query().Get("digest") != digest {
						t.Error("download did not carry the digest that was offered")
					}
					if tc.problem == "wrong package bytes" {
						io.WriteString(w, "tampered-install")
						return
					}
					io.WriteString(w, payload)
				default:
					t.Errorf("unexpected KeyDock route %q", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := *server.Client()
			client.Transport = testTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host != "api.github.com" {
					return server.Client().Transport.RoundTrip(req)
				}
				if req.URL.String() != releasesByTag+version || req.Header.Get("Authorization") != "" {
					t.Errorf("unexpected release authority request %s", req.URL)
				}
				officialDigest := digest
				if tc.problem == "wrong publisher digest" {
					officialDigest = strings.Repeat("0", 64)
				}
				response := fmt.Sprintf(`{"tag_name":"v%s","assets":[{"name":%q,"state":"uploaded","digest":"sha256:%s","size":%d,"browser_download_url":%q}]}`,
					version, target.filename, officialDigest, len(payload), releaseAssets+version+"/"+target.filename)
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(response))}, nil
			})
			release, err := check(t.Context(), &client, server.URL+"/edge/", "0.9.9", target)
			if err != nil || release == nil || release.Version != version || downloads.Load() != 0 {
				t.Fatalf("check result: %v, %v, download count %d", release, err, downloads.Load())
			}
			path, err := download(t.Context(), &client, release)
			if tc.problem != "" {
				if path != "" || err == nil || (tc.problem == "wrong publisher digest" && downloads.Load() != 0) {
					t.Fatalf("invalid update accepted: %q, %v, download count %d", path, err, downloads.Load())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(filepath.Dir(path))
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if !release.Verify(file) || filepath.Base(path) != target.filename || downloads.Load() != 1 {
				t.Fatalf("invalid saved package: %q, download count %d", path, downloads.Load())
			}
		})
	}
}

func TestUpdateCheckRejectsUnsafeOrInconsistentResults(t *testing.T) {
	target := platforms["darwin/arm64"]
	digest := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, response string
		wantError      bool
	}{
		{"up to date", `{"update_available":false,"latest_version":"0.4.1"}`, false},
		{"server behind", `{"update_available":false,"latest_version":"0.4.0"}`, false},
		{"missing flag", `{"latest_version":"0.4.1"}`, true},
		{"downgrade", `{"update_available":true,"latest_version":"0.4.0"}`, true},
		{"bad version", `{"update_available":false,"latest_version":"0.4.1-beta"}`, true},
		{"external installer", fmt.Sprintf(`{"update_available":true,"latest_version":"0.4.2","sha256":%q,"download_path":"https://elsewhere.example/setup"}`, digest), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.response))}, nil
			})}
			release, err := check(context.Background(), client, "https://keydock.example", "0.4.1", target)
			if (err != nil) != tc.wantError || release != nil {
				t.Fatalf("unexpected result: %v, %v", release, err)
			}
		})
	}
}
