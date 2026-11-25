package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yungor-ai/yungor-keydock-connect/internal/diagnostics"
)

func TestHTTPSProbeRecordsWhereTheTunnelFails(t *testing.T) {
	cases := []struct {
		name          string
		connectStatus int
		failedStage   string
	}{
		{"proxy rejected CONNECT", http.StatusBadGateway, "proxy.test_connect"},
		{"TLS failed after CONNECT", http.StatusOK, "proxy.test_tls"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const private = "private-connection-fixture"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodConnect {
					t.Errorf("expected a local CONNECT request, got %s", r.Method)
				}
				conn, buf, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				_, err = fmt.Fprintf(buf, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\n\r\n", tc.connectStatus, private)
				if err == nil {
					err = buf.Flush()
				}
				if err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()

			dir := t.TempDir()
			out := filepath.Join(dir, "report.json")
			log := diagnostics.New(dir)
			m := New(dir, "")
			m.SetDiagnostics(log)
			err := m.checkHTTPS(context.Background(), server.Listener.Addr().(*net.TCPAddr).Port, "https://"+private+".invalid", nil)
			if err == nil || err.Error() != "SS2022 代理未能连接到 HTTPS 测试服务" {
				t.Fatalf("unexpected UI result: %v", err)
			}
			if err := log.Export(out, diagnostics.State{}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), private) {
				t.Fatal("probe hostname or raw CONNECT response leaked to diagnostics")
			}
			var report struct {
				Events []struct {
					Stage      string `json:"stage"`
					Outcome    string `json:"outcome"`
					HTTPStatus int    `json:"http_status"`
				} `json:"events"`
			}
			if err := json.Unmarshal(data, &report); err != nil {
				t.Fatal(err)
			}
			var localDial, failedTunnel, failedHTTPS, connectStatus bool
			for _, event := range report.Events {
				localDial = localDial || (event.Stage == "proxy.test_local_dial" && event.Outcome == "ok")
				failedTunnel = failedTunnel || (event.Stage == tc.failedStage && event.Outcome == "failed")
				failedHTTPS = failedHTTPS || (event.Stage == "proxy.test_https" && event.Outcome == "failed")
				connectStatus = connectStatus || (event.Stage == "proxy.test_connect" && event.HTTPStatus == tc.connectStatus)
			}
			if !localDial || !failedTunnel || !failedHTTPS || !connectStatus {
				t.Fatalf("missing CONNECT status or failure stage: %+v", report.Events)
			}
		})
	}
}
