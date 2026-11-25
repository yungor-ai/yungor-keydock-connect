package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

func (m *Manager) checkHTTPS(ctx context.Context, port int, target string, tlsConfig *tls.Config) error {
	started := time.Now()
	type checkStage struct {
		name           string
		elapsed        time.Duration
		err            error
		status, wanted int
	}
	// Transport callbacks can run even while the request is being canceled.
	// Keep disk writes out of the callbacks and preserve their measured timings.
	var mu sync.Mutex
	stages := make([]checkStage, 0, 4)
	observe := func(name string, start time.Time, err error, status, wanted int) {
		mu.Lock()
		stages = append(stages, checkStage{name, time.Since(start), err, status, wanted})
		mu.Unlock()
	}
	defer func() {
		mu.Lock()
		completed := stages
		stages = nil
		mu.Unlock()
		for _, stage := range completed {
			m.logHTTPStage(stage.name, stage.elapsed, stage.err, stage.status, stage.wanted)
		}
	}()
	local := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	tr := &http.Transport{
		Proxy:               http.ProxyURL(local),
		TLSClientConfig:     tlsConfig,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: 8 * time.Second,
		OnProxyConnectResponse: func(_ context.Context, _ *url.URL, _ *http.Request, resp *http.Response) error {
			observe("proxy.test_connect", started, nil, resp.StatusCode, http.StatusOK)
			return nil
		},
	}
	defer tr.CloseIdleConnections()
	var dialStarted, handshakeStarted atomic.Pointer[time.Time]
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		ConnectStart: func(_, _ string) {
			started := time.Now()
			dialStarted.Store(&started)
		},
		ConnectDone: func(_, _ string, err error) {
			if begin := dialStarted.Load(); begin != nil {
				observe("proxy.test_local_dial", *begin, err, 0, 0)
			}
		},
		TLSHandshakeStart: func() {
			started := time.Now()
			handshakeStarted.Store(&started)
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			begin := handshakeStarted.Load()
			if begin == nil {
				begin = &started
			}
			observe("proxy.test_tls", *begin, err, 0, 0)
		},
	})
	c := http.Client{Transport: tr, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		observe("proxy.test_https", started, err, 0, http.StatusNoContent)
		return errors.New("代理检查地址无效")
	}
	resp, err := c.Do(req)
	if err != nil {
		observe("proxy.test_https", started, err, 0, http.StatusNoContent)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("SS2022 代理未能连接到 HTTPS 测试服务")
	}
	defer resp.Body.Close()
	observe("proxy.test_https", started, nil, resp.StatusCode, http.StatusNoContent)
	if resp.StatusCode != http.StatusNoContent {
		return errors.New("代理测试服务未返回预期的 204 响应")
	}
	return nil
}
