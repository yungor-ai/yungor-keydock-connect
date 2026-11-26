package desktop

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yungor-ai/yungor-keydock-connect/internal/networkcheck"
	"github.com/yungor-ai/yungor-keydock-connect/internal/proxy"
)

type networkProxy struct {
	fixtureProxy
	active atomic.Bool
}

func (p *networkProxy) StartMode(string, proxy.Mode) error {
	p.active.Store(true)
	return nil
}

func (p *networkProxy) ActiveMode() proxy.Mode {
	if p.active.Load() {
		return proxy.ModeSystem
	}
	return ""
}

func (p *networkProxy) ProxyAddress() string {
	if p.active.Load() {
		return "127.0.0.1:17890"
	}
	return ""
}

func TestWatchNetworkReplacesStartupResultsOnProxyChange(t *testing.T) {
	c, _ := deviceTest(t)
	c.proxy = &networkProxy{}
	if err := c.persist(enrollment{ServerURL: address, Profile: assigned}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reports := make(chan Check, 8)
	startupCanceled := make(chan struct{})
	started := make(chan string, 2)
	probe := func(ctx context.Context, client *http.Client, server string) networkcheck.Result {
		transport := client.Transport.(*http.Transport)
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://localhost/generate_204", nil)
		via, err := transport.Proxy(request)
		if err != nil || server != address {
			t.Errorf("probe route: proxy=%v server=%q err=%v", via, server, err)
		}
		if via == nil {
			started <- "direct"
			<-ctx.Done()
			close(startupCanceled)
			return networkcheck.Result{ExitIP: "192.0.2.10", ServerIPs: []string{"192.0.2.10"}, IPMatch: true}
		}
		started <- via.String()
		return networkcheck.Result{GoogleReachable: true, ExitIP: "192.0.2.11", ServerIPs: []string{"192.0.2.10"}}
	}
	go c.watchNetwork(ctx, func(result Check) { reports <- result }, probe)
	assertProbePath := func(want string) {
		t.Helper()
		select {
		case got := <-started:
			if got != want {
				t.Fatalf("probe path = %q, want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatal("expected a network probe")
		}
	}
	assertProbePath("direct")
	if err := c.SetProxyMode(true, proxy.ModeSystem); err != nil {
		t.Fatal(err)
	}
	assertProbePath("http://127.0.0.1:17890")
	select {
	case <-startupCanceled:
	case <-ctx.Done():
		t.Fatal("startup probe was not canceled")
	}
	var google, egress Check
	for google.Status == "" || egress.Status == "" {
		select {
		case result := <-reports:
			if result.Status == "running" {
				continue
			}
			switch result.Stage {
			case "google":
				google = result
			case "egress":
				egress = result
			}
		case <-ctx.Done():
			t.Fatal("network results were not reported")
		}
	}
	if google.Status != "ok" || egress.Status != "warning" || !strings.Contains(egress.Message, "192.0.2.11") {
		t.Fatalf("unexpected current route results: google=%+v egress=%+v", google, egress)
	}
}

func TestReportNetworkResultWhenAnIPv4AddressIsMissing(t *testing.T) {
	for _, test := range []struct {
		name, server, status, message string
		result                        networkcheck.Result
	}{
		{
			name:    "exit has no IPv4",
			server:  "https://192.0.2.10",
			status:  "failed",
			message: "请检查当前网络或代理",
			result:  networkcheck.Result{ServerIPs: []string{"192.0.2.10"}},
		},
		{
			name:    "server has no IPv4",
			server:  "https://[2001:db8::20]",
			status:  "skipped",
			message: "请联系管理员",
			result:  networkcheck.Result{ExitIP: "192.0.2.10"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got Check
			reportNetworkResult(test.server, test.result, func(check Check) {
				if check.Stage == "egress" {
					got = check
				}
			})
			if got.Status != test.status || !strings.Contains(got.Message, "IPv4") || !strings.Contains(got.Message, test.message) {
				t.Fatalf("egress report = %+v, want %s with IPv4 guidance %q", got, test.status, test.message)
			}
		})
	}
}
