package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	zip "go.foxforensics.eu/go-zip/zip"
)

const testKey = "synthetic-key-private"
const testSubscription = "https://access.example.test/sub/synthetic-private"

type testProxy struct {
	active       bool
	subscription string
}

func (p *testProxy) Start(s string) error { p.subscription = s; p.active = true; return nil }
func (p *testProxy) Stop() error          { p.active = false; return nil }
func (p *testProxy) Active() bool         { return p.active }

func privateBundle(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Encrypt("TokenHub/个人接入信息.json", "synthetic-password", zip.AES256Encryption)
	if err != nil {
		t.Fatal(err)
	}
	err = json.NewEncoder(f).Encode(map[string]any{"name": "测试用户", "device_name": "设计部 · 测试电脑", "api_base_url": "https://model.example.test/v1", "api_key": testKey, "clash_subscription_url": testSubscription})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func syntheticApp(t *testing.T, host string) (*App, *testProxy, func() bool) {
	t.Helper()
	dir := t.TempDir()
	pr := &testProxy{}
	configured, quit := false, false
	seal := func(v []byte) ([]byte, error) {
		o := make([]byte, len(v))
		for i := range v {
			o[i] = v[i] ^ 0x56
		}
		return o, nil
	}
	ops := Backend{Apply: func(home, state, base, model, key string) error {
		if key != testKey || model != "demo-model" {
			return errors.New("unexpected synthetic configuration")
		}
		configured = true
		return nil
	}, Restore: func(string, string) error { configured = false; return nil }, Configured: func(string, string) bool { return configured }, Matches: func(string, string, string, string, string) bool { return configured }, Protect: seal, Unprotect: seal, Launch: func() error { return nil }}
	a, err := New(host, dir, dir, pr, ops, func() { quit = true })
	if err != nil {
		t.Fatal(err)
	}
	return a, pr, func() bool { return quit }
}
func (a *App) call(t *testing.T, route, contentType string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "http://"+a.host+"/api/"+route, body)
	r.Header.Set("Origin", "http://"+a.host)
	r.Header.Set("X-KeyDock-Session", a.token)
	r.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if strings.Contains(w.Body.String(), testKey) || strings.Contains(w.Body.String(), testSubscription) {
		t.Fatal("secret returned by local API")
	}
	return w
}
func TestImportConfigureProxyRestoreAndQuit(t *testing.T) {
	a, p, quit := syntheticApp(t, "127.0.0.1:3132")
	var form bytes.Buffer
	m := multipart.NewWriter(&form)
	m.WriteField("password", "synthetic-password")
	f, _ := m.CreateFormFile("package", "access.zip")
	f.Write(privateBundle(t))
	m.Close()
	check := func(w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("request failed %d %s", w.Code, w.Body.String())
		}
	}
	check(a.call(t, "import", m.FormDataContentType(), &form))
	raw, err := os.ReadFile(filepath.Join(a.stateDir, "device.bin"))
	if err != nil || bytes.Contains(raw, []byte(testKey)) {
		t.Fatal("invalid device persistence")
	}
	reopened, err := New(a.host, a.home, a.stateDir, p, a.ops, func() {})
	if err != nil || reopened.profile.Key != testKey {
		t.Fatal("reopen failed")
	}
	check(a.call(t, "apply", "application/json", strings.NewReader(`{"model":"demo-model"}`)))
	if !a.status().Configured {
		t.Fatal("not configured")
	}
	check(a.call(t, "proxy/on", "", nil))
	if !p.active || p.subscription != testSubscription {
		t.Fatal("wrong proxy subscription")
	}
	if w := a.call(t, "import", "", nil); w.Code != 400 {
		t.Fatal("could replace running proxy subscription")
	}
	check(a.call(t, "restore", "", nil))
	if a.status().Configured {
		t.Fatal("restore did not run")
	}
	check(a.call(t, "quit", "", nil))
	if !quit() || p.active {
		t.Fatal("quit failed to stop managed proxy")
	}
}
func TestWindowAccess(t *testing.T) {
	a, _, _ := syntheticApp(t, "127.0.0.1:3322")
	for _, v := range []struct {
		host, origin, token, method string
		want                        int
	}{{a.host, "https://example.test", a.token, "POST", 403}, {"example.test", "http://" + a.host, a.token, "POST", 403}, {a.host, "http://" + a.host, "", "POST", 403}, {a.host, "http://" + a.host, a.token, "GET", 405}} {
		r := httptest.NewRequest(v.method, "http://"+v.host+"/api/state", nil)
		r.Header.Set("Origin", v.origin)
		r.Header.Set("X-KeyDock-Session", v.token)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != v.want {
			t.Errorf("want %d got %d", v.want, w.Code)
		}
	}
	for _, target := range []string{"/", "/desk.css", "/desk.js"} {
		r := httptest.NewRequest("GET", "http://"+a.host+target, nil)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != 200 || strings.Contains(w.Body.String(), a.token) {
			t.Fatal("asset exposed token or failed")
		}
	}
	if u, _ := url.Parse(a.URL()); u.RawQuery != "" || u.Fragment != a.token {
		t.Fatal("browser bootstrap")
	}
}
func TestHTTPSMigration(t *testing.T) {
	if migratedBaseURL("http://144.34.153.220/cliproxy/v1") != "https://144.34.153.220/v1" {
		t.Fatal("known historical route")
	}
	if _, err := gatewayURL(migratedBaseURL("http://gateway.example.test/v1")); err == nil {
		t.Fatal("arbitrary HTTP accepted")
	}
}

var _ http.Handler = (*App)(nil)
