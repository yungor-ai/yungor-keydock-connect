package desktop

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/yungor-ai/yungor-keydock-connect/internal/networkcheck"
	"github.com/yungor-ai/yungor-keydock-connect/internal/proxy"
)

type networkRoute struct {
	server, proxyAddress string
	mode                 proxy.Mode
}

func (c *Controller) networkRoute() networkRoute {
	return networkRoute{server: c.snapshot().ServerURL, mode: c.proxy.ActiveMode(), proxyAddress: c.ProxyAddress()}
}

// RecheckNetwork requests a fresh measurement for the current server and route.
func (c *Controller) RecheckNetwork() {
	select {
	case c.networkChanged <- struct{}{}:
	default:
	}
}

// WatchNetwork reports the startup checks and refreshes them when the
// enrolled server or KeyDock proxy changes. One window owns the watcher.
func (c *Controller) WatchNetwork(ctx context.Context, report func(Check)) {
	c.watchNetwork(ctx, report, networkcheck.Check)
}

func (c *Controller) watchNetwork(ctx context.Context, report func(Check), probe func(context.Context, *http.Client, string) networkcheck.Result) {
	// A worker can exit without a UI action. Poll its local state and only
	// send another external request when the route changes.
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	var route networkRoute
	var results <-chan networkcheck.Result
	var cancel context.CancelFunc
	start := func(next networkRoute) {
		if cancel != nil {
			cancel()
		}
		route = next
		report(Check{Stage: "google", Status: "running", Message: "正在通过 TCP HTTPS 检测 www.google.com…"})
		report(Check{Stage: "egress", Status: "running", Message: "正在通过 TCP HTTPS 查询出口 IPv4…"})
		probeCtx, stop := context.WithCancel(ctx)
		cancel = stop
		out := make(chan networkcheck.Result, 1)
		results = out
		go func() {
			out <- next.check(probeCtx, probe)
		}()
	}
	start(c.networkRoute())
	defer func() { cancel() }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.networkChanged:
			start(c.networkRoute())
		case <-ticker.C:
			if next := c.networkRoute(); next != route {
				start(next)
			}
		case result := <-results:
			if ctx.Err() != nil {
				return
			}
			select {
			case <-c.networkChanged:
				start(c.networkRoute())
				continue
			default:
			}
			if next := c.networkRoute(); next != route {
				start(next)
				continue
			}
			results = nil
			reportNetworkResult(route.server, result, report)
		}
	}
}

func (r networkRoute) check(ctx context.Context, probe func(context.Context, *http.Client, string) networkcheck.Result) networkcheck.Result {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	switch r.mode {
	case proxy.ModeLocal, proxy.ModeSystem:
		if r.proxyAddress == "" {
			return networkcheck.Result{}
		}
		transport.Proxy = http.ProxyURL(&url.URL{Scheme: "http", Host: r.proxyAddress})
	case proxy.ModeTUN:
		transport.Proxy = nil
	}
	return probe(ctx, &http.Client{Transport: transport}, r.server)
}

func reportNetworkResult(server string, result networkcheck.Result, report func(Check)) {
	google := Check{Stage: "google", Status: "failed", Message: "www.google.com（TCP HTTPS）"}
	if result.GoogleReachable {
		google.Status = "ok"
	}
	report(google)
	if result.ExitIP == "" {
		report(Check{Stage: "egress", Status: "failed", Message: "TCP HTTPS 未获取到出口 IPv4"})
		return
	}
	egress := Check{Stage: "egress", Status: "warning", Message: result.ExitIP + "（TCP HTTPS）"}
	switch {
	case server == "":
		egress.Status, egress.Message = "skipped", result.ExitIP+"（TCP HTTPS，尚未配置 KeyDock）"
	case len(result.ServerIPs) == 0:
		egress.Status, egress.Message = "skipped", result.ExitIP+"（TCP HTTPS，KeyDock 无可用 IPv4）"
	case result.IPMatch:
		egress.Status = "ok"
	}
	report(egress)
}
