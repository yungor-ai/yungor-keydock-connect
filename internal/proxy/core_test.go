package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func reservePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
func runFixtureCore(t *testing.T, core string, config []byte, port int) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	cmd := exec.CommandContext(ctx, core, "-d", t.TempDir(), "-f", "-")
	cmd.Stdin = bytes.NewReader(config)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	stop := sync.OnceFunc(func() { cancel(); <-done })
	t.Cleanup(stop)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatalf("fixture core exited: %s", logs.String())
		default:
		}
		c, err := net.DialTimeout("tcp4", "127.0.0.1:"+strconv.Itoa(port), 100*time.Millisecond)
		if err == nil {
			c.Close()
			return stop
		}
		time.Sleep(40 * time.Millisecond)
	}
	stop()
	t.Fatalf("fixture core did not listen: %s", logs.String())
	return nil
}

// Both peers and the HTTP destination run on loopback. A request must fail
// after the SS2022 peer exits, detecting accidental direct fallback.
func TestSS2022DataPath(t *testing.T) {
	core := os.Getenv("KEYDOCK_PROFILE_CORE")
	if core == "" {
		core = os.Getenv("KEYDOCK_TEST_CORE")
	}
	if core == "" {
		t.Skip("set a native pinned core to exercise the encrypted loopback path")
	}
	core, err := filepath.Abs(core)
	if err != nil {
		t.Fatal(err)
	}
	ssPort, stopServer := fixturePeer(t, core)
	clientPort := reservePort(t)
	// No external groups, GEO downloads, or health probes in this data path.
	source := strings.Replace(peerSubscription(ssPort), "MATCH,test-node", "MATCH,DIRECT", 1)
	clientProfile, err := buildRouting([]byte(source), clientPort, true)
	if err != nil {
		t.Fatal(err)
	}
	runFixtureCore(t, core, clientProfile, clientPort)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "encrypted loopback") }))
	defer target.Close()
	u, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(clientPort))
	tr := &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	httpClient := http.Client{Transport: tr, Timeout: 4 * time.Second}
	check := func() bool {
		resp, err := httpClient.Get(target.URL)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return err == nil && resp.StatusCode == 200 && string(body) == "encrypted loopback"
	}
	if !check() {
		t.Fatal("SS2022 test traffic did not reach the loopback endpoint")
	}
	stopServer()
	if check() {
		t.Fatal("encrypted route bypassed the stopped SS2022 server")
	}
}

func fixturePeer(t *testing.T, core string) (int, func()) {
	t.Helper()
	ssPort := reservePort(t)
	server := fmt.Sprintf(`mode: direct
log-level: warning
listeners:
  - name: fixture-ss
    type: shadowsocks
    listen: 127.0.0.1
    port: %d
    cipher: 2022-blake3-aes-128-gcm
    password: "AAAAAAAAAAAAAAAAAAAAAA=="
    users: {test-device: "AQEBAQEBAQEBAQEBAQEBAQ=="}
`, ssPort)
	return ssPort, runFixtureCore(t, core, []byte(server), ssPort)

}

func peerSubscription(port int) string {
	source := strings.ReplaceAll(fixture, "192.0.2.1", "127.0.0.1")
	source = strings.ReplaceAll(source, "24443", strconv.Itoa(port))
	return strings.SplitN(source, "proxy-groups:", 2)[0] + "rules:\n  - MATCH,test-node\n"
}
