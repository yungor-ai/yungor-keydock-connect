package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yungor-ai/yungor-keydock-connect/internal/connect"
	"github.com/yungor-ai/yungor-keydock-connect/internal/diagnostics"
	"github.com/yungor-ai/yungor-keydock-connect/internal/proxy"
)

type fixtureProxy struct {
	mode       proxy.Mode
	tested     string
	refreshed  string
	checks     int
	err        error
	refreshErr error
}

func (p *fixtureProxy) ActiveMode() proxy.Mode                    { return p.mode }
func (p *fixtureProxy) CheckMode(proxy.Mode) error                { return nil }
func (p *fixtureProxy) StartMode(_ string, mode proxy.Mode) error { p.mode = mode; return nil }
func (p *fixtureProxy) Stop() error                               { p.mode = ""; return nil }
func (p *fixtureProxy) Test(_ context.Context, s string) error    { p.tested = s; return p.err }
func (p *fixtureProxy) Refresh(_ context.Context, s string) error {
	p.refreshed = s
	p.checks++
	return p.refreshErr
}

var assigned = connect.Profile{DeviceName: "合成设备", Key: "model-test-key", BaseURL: "https://model.example.test/v1", Model: "model-test", SubscriptionURL: "https://ss.example.test/sub/test-private"}

const address = "https://keydock.example.test"
const enrollmentKey = "device-only-test-secret"

func sealTest(b []byte) ([]byte, error) {
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = v ^ 0x42
	}
	return out, nil
}

func deviceTest(t *testing.T) (*Controller, *fixtureProxy) {
	t.Helper()
	target := ""
	proxy := &fixtureProxy{}
	ops := Backend{Protect: sealTest, Unprotect: sealTest,
		Fetch: func(ctx context.Context, url, key string) (connect.DeviceConfig, error) {
			if url != address || key != enrollmentKey {
				return connect.DeviceConfig{}, errors.New("bad fixture pairing")
			}
			return connect.DeviceConfig{Profile: assigned}, nil
		},
		TestModel: func(ctx context.Context, p connect.Profile) error {
			if p.Key != assigned.Key {
				t.Fatal("wrong model credential")
			}
			return nil
		},
		Apply: func(_, _, _, model, key string) error {
			if key != assigned.Key {
				t.Fatal("device credential passed to Codex")
			}
			target = model
			return nil
		},
		Matches:    func(_, _, _, model, key string) bool { return key == assigned.Key && target == model },
		Configured: func(string, string) bool { return target != "" }, Restore: func(string, string) error { target = ""; return nil }, Launch: func() error { return nil },
	}
	stateDir := t.TempDir()
	c, err := NewController(t.TempDir(), stateDir, proxy, ops, diagnostics.New(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	return c, proxy
}

func TestNativeEnrollmentLifecycle(t *testing.T) {
	c, p := deviceTest(t)
	var stages []string
	err := c.Connect(context.Background(), address+"/", enrollmentKey, func(v Check) { stages = append(stages, v.Stage+":"+v.Status) })
	want := []string{"device:running", "device:ok", "codex:running", "codex:ok", "model:running", "model:ok", "proxy:running", "proxy:ok"}
	if err != nil || !reflect.DeepEqual(stages, want) {
		t.Fatalf("connect: %v, stages %v", err, stages)
	}
	if p.mode != "" || p.tested != assigned.SubscriptionURL {
		t.Fatal("diagnostics activated system proxy or used wrong subscription")
	}
	state := c.State()
	if !state.Paired || !state.Configured || state.ServerURL != address || state.Device != assigned.DeviceName {
		t.Fatal("unexpected state")
	}
	info, _ := json.Marshal(state)
	data, err := os.ReadFile(c.devicePath())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{assigned.Key, assigned.SubscriptionURL, enrollmentKey} {
		if bytes.Contains(info, []byte(secret)) || bytes.Contains(data, []byte(secret)) {
			t.Fatal("credential exposed")
		}
	}
	reopened, err := NewController(c.home, c.stateDir, p, c.ops, diagnostics.New(c.stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Connect(context.Background(), address, "", nil); err != nil {
		t.Fatal(err)
	}
	if err = reopened.Connect(context.Background(), address+"/other", "", nil); err == nil {
		t.Fatal("exported device credential to another base")
	}
	if err = reopened.SetProxyMode(true, proxy.ModeSystem); err != nil || p.mode != proxy.ModeSystem {
		t.Fatal("proxy enable failed")
	}
	if err = reopened.Restore(); err != nil || reopened.State().Configured {
		t.Fatal("restore failed")
	}
	if err = reopened.Close(); err != nil || p.mode != "" {
		t.Fatal("tray exit did not stop proxy")
	}
}

func TestPartialResultsAndRetry(t *testing.T) {
	c, p := deviceTest(t)
	c.ops.TestModel = func(context.Context, connect.Profile) error { return errors.New("网关无响应") }
	seen := map[string]string{}
	if c.Connect(context.Background(), address, enrollmentKey, func(v Check) { seen[v.Stage] = v.Status }) == nil || seen["model"] != "failed" || seen["proxy"] != "ok" || !c.State().Configured {
		t.Fatal("partial diagnostics hidden")
	}
	prev := c.snapshot()
	c.ops.Fetch = func(context.Context, string, string) (connect.DeviceConfig, error) {
		return connect.DeviceConfig{}, errors.New("未授权")
	}
	if c.Connect(context.Background(), address, "invalid", nil) == nil || c.snapshot() != prev {
		t.Fatal("failed exchange changed existing device")
	}
	c.ops.Fetch = func(context.Context, string, string) (connect.DeviceConfig, error) {
		q := assigned
		q.SubscriptionURL = ""
		return connect.DeviceConfig{Profile: q}, nil
	}
	p.mode = proxy.ModeTUN
	if err := c.Connect(context.Background(), address, enrollmentKey, func(v Check) { seen[v.Stage] = v.Status }); err == nil || seen["proxy"] != "skipped" || p.mode != "" || c.State().Subscription {
		t.Fatal("model-only reassignment failed")
	}
}

func TestProxyFailureOffersClockGuidance(t *testing.T) {
	c, p := deviceTest(t)
	for _, tc := range []struct {
		name  string
		skew  time.Duration
		err   error
		parts []string
	}{
		{"slow clock in customer report", -31324 * time.Millisecond, errors.New("连接中断"), []string{"慢了 30 秒以上", "立即同步"}},
		{"fast clock", 85 * time.Second, errors.New("连接中断"), []string{"快了 30 秒以上", "立即同步"}},
		{"clock difference unconfirmed", 0, errors.New("连接中断"), []string{"立即同步", "30 秒"}},
		{"proxy works despite clock difference", -31324 * time.Millisecond, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c.ops.Fetch = func(context.Context, string, string) (connect.DeviceConfig, error) {
				return connect.DeviceConfig{Profile: assigned, ClockSkew: tc.skew}, nil
			}
			p.err = tc.err
			var proxyCheck Check
			err := c.Connect(context.Background(), address, enrollmentKey, func(check Check) {
				if check.Stage == "proxy" {
					proxyCheck = check
				}
			})
			if !errors.Is(err, tc.err) {
				t.Fatalf("unexpected connection error: %v", err)
			}
			if tc.err == nil && (proxyCheck.Status != "ok" || proxyCheck.Hint != "") {
				t.Fatalf("healthy proxy received a trouble hint: %+v", proxyCheck)
			}
			for _, part := range tc.parts {
				if proxyCheck.Status != "failed" || !strings.Contains(proxyCheck.Hint, part) {
					t.Fatalf("expected %q in failed proxy guidance: %+v", part, proxyCheck)
				}
			}
		})
	}
}

func TestExportDiagnosticsAfterProxyFailure(t *testing.T) {
	c, p := deviceTest(t)
	if err := c.Connect(context.Background(), address, enrollmentKey, nil); err != nil {
		t.Fatal(err)
	}
	p.mode = proxy.ModeSystem
	p.err = errors.New("bad upstream at " + assigned.SubscriptionURL)
	if err := c.Connect(context.Background(), address, enrollmentKey, nil); err == nil {
		t.Fatal("expected a failed proxy test")
	}
	reopened, err := NewController(c.home, c.stateDir, p, c.ops, diagnostics.New(c.stateDir))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := reopened.ExportDiagnostics(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{assigned.Key, assigned.SubscriptionURL, enrollmentKey, address, assigned.BaseURL, assigned.DeviceName, c.home} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatal("diagnostics include private device data")
		}
	}
	var exported struct {
		State  diagnostics.State   `json:"state"`
		Events []diagnostics.Event `json:"events"`
	}
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatal(err)
	}
	if exported.State != (diagnostics.State{Paired: true, CodexConfigured: true, SystemProxyActive: true, SubscriptionAssigned: true, ProxyMode: string(proxy.ModeSystem)}) {
		t.Fatalf("unexpected diagnostic state: %+v", exported.State)
	}
	var completed []string
	for _, event := range exported.Events {
		if event.Outcome != "start" {
			completed = append(completed, event.Stage+":"+event.Outcome)
		}
	}
	want := []string{"device:ok", "codex:ok", "model:ok", "proxy:ok", "device:ok", "codex:ok", "model:ok", "proxy:failed"}
	if !reflect.DeepEqual(completed, want) {
		t.Fatalf("unexpected diagnostics: %v", completed)
	}
}

func TestSubscriptionRefreshOnlyUsesActiveModeAndEnrollment(t *testing.T) {
	c, p := deviceTest(t)
	ctx := context.Background()
	if err := c.Connect(ctx, address, enrollmentKey, nil); err != nil {
		t.Fatal(err)
	}
	if c.refreshActiveSubscription(ctx) || p.checks != 0 {
		t.Fatal("inactive proxy requested a subscription refresh")
	}
	if err := c.SetProxyMode(true, proxy.ModeTUN); err != nil {
		t.Fatal(err)
	}
	firstUpdatedAt := c.State().SubscriptionUpdatedAt
	if firstUpdatedAt.IsZero() {
		t.Fatal("first successful subscription load did not set an update time")
	}
	p.refreshErr = errors.New("upstream unavailable")
	if !c.refreshActiveSubscription(ctx) || p.refreshed != assigned.SubscriptionURL || p.checks != 1 {
		t.Fatal("did not refresh the current enrollment")
	}
	state := c.State()
	if state.ProxyMode != proxy.ModeTUN || !state.SubscriptionRefreshFailed || state.SubscriptionCheckedAt.IsZero() {
		t.Fatalf("an unsuccessful refresh should leave the active mode on and report its status: %+v", state)
	}
	if !state.SubscriptionUpdatedAt.Equal(firstUpdatedAt) {
		t.Fatal("an unsuccessful refresh replaced the successful update time")
	}
	p.refreshErr = nil
	if !c.refreshActiveSubscription(ctx) || c.State().SubscriptionRefreshFailed {
		t.Fatal("an ordinary successful refresh did not clear the error status")
	}
	updatedAt := c.State().SubscriptionUpdatedAt
	if updatedAt.IsZero() || updatedAt.Before(firstUpdatedAt) {
		t.Fatal("a successful refresh did not save its update time")
	}
	if err := c.SetProxyMode(false, proxy.ModeTUN); err != nil {
		t.Fatal(err)
	}
	if state := c.State(); state.Proxy || !state.SubscriptionCheckedAt.IsZero() || !state.SubscriptionUpdatedAt.Equal(updatedAt) {
		t.Fatalf("mode and refresh state survived stop: %+v", state)
	}
	reopened, err := NewController(c.home, c.stateDir, p, c.ops, diagnostics.New(c.stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Connect(ctx, address, "", nil); err != nil {
		t.Fatal(err)
	}
	if !reopened.State().SubscriptionUpdatedAt.Equal(updatedAt) {
		t.Fatal("the update time was lost after reopening and reconnecting the same subscription")
	}
	reopened.ops.Fetch = func(context.Context, string, string) (connect.DeviceConfig, error) {
		next := assigned
		next.SubscriptionURL = assigned.SubscriptionURL + "/new"
		return connect.DeviceConfig{Profile: next}, nil
	}
	if err := reopened.Connect(ctx, address, "", nil); err != nil {
		t.Fatal(err)
	}
	if !reopened.State().SubscriptionUpdatedAt.IsZero() {
		t.Fatal("the new subscription inherited another subscription's update time")
	}
}

func TestUnpairedV1ProfileAndApplyFailure(t *testing.T) {
	c, p := deviceTest(t)
	b, _ := json.Marshal(assigned)
	raw, _ := sealTest(b)
	if err := os.WriteFile(filepath.Join(c.stateDir, "device.bin"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	old, err := NewController(c.home, c.stateDir, p, c.ops, diagnostics.New(c.stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if old.State().Device != assigned.DeviceName || old.State().Paired {
		t.Fatal("legacy profile not recognized")
	}
	old.ops.Apply = func(string, string, string, string, string) error { return errors.New("配置冲突") }
	seen := map[string]string{}
	err = old.Connect(context.Background(), address, enrollmentKey, func(v Check) { seen[v.Stage] = v.Status })
	if err == nil || seen["codex"] != "failed" || seen["model"] != "skipped" || seen["proxy"] != "ok" {
		t.Fatal("local config error hidden")
	}
}
