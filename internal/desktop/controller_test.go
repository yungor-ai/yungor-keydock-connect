package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yungor-ai/yungor-keydock-connect/internal/connect"
)

type fixtureProxy struct {
	active bool
	tested string
	err    error
}

func (p *fixtureProxy) Active() bool                           { return p.active }
func (p *fixtureProxy) Start(string) error                     { p.active = true; return nil }
func (p *fixtureProxy) Stop() error                            { p.active = false; return nil }
func (p *fixtureProxy) Test(_ context.Context, s string) error { p.tested = s; return p.err }

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
		Fetch: func(ctx context.Context, url, key string) (connect.Profile, error) {
			if url != address || key != enrollmentKey {
				return connect.Profile{}, errors.New("bad fixture pairing")
			}
			return assigned, nil
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
	c, err := NewController(t.TempDir(), t.TempDir(), proxy, ops)
	if err != nil {
		t.Fatal(err)
	}
	return c, proxy
}

func TestNativeEnrollmentLifecycle(t *testing.T) {
	c, proxy := deviceTest(t)
	var stages []string
	err := c.Connect(context.Background(), address+"/", enrollmentKey, func(v Check) { stages = append(stages, v.Stage+":"+v.Status) })
	want := []string{"device:running", "device:ok", "codex:running", "codex:ok", "model:running", "model:ok", "proxy:running", "proxy:ok"}
	if err != nil || !reflect.DeepEqual(stages, want) {
		t.Fatalf("connect: %v, stages %v", err, stages)
	}
	if proxy.active || proxy.tested != assigned.SubscriptionURL {
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
	reopened, err := NewController(c.home, c.stateDir, proxy, c.ops)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Connect(context.Background(), address, "", nil); err != nil {
		t.Fatal(err)
	}
	if err = reopened.Connect(context.Background(), address+"/other", "", nil); err == nil {
		t.Fatal("exported device credential to another base")
	}
	if err = reopened.SetProxy(true); err != nil || !proxy.active {
		t.Fatal("proxy enable failed")
	}
	if err = reopened.Restore(); err != nil || reopened.State().Configured {
		t.Fatal("restore failed")
	}
	if err = reopened.Close(); err != nil || proxy.active {
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
	c.ops.Fetch = func(context.Context, string, string) (connect.Profile, error) {
		return connect.Profile{}, errors.New("未授权")
	}
	if c.Connect(context.Background(), address, "invalid", nil) == nil || c.snapshot() != prev {
		t.Fatal("failed exchange changed existing device")
	}
	c.ops.Fetch = func(context.Context, string, string) (connect.Profile, error) {
		q := assigned
		q.SubscriptionURL = ""
		return q, nil
	}
	p.active = true
	if err := c.Connect(context.Background(), address, enrollmentKey, func(v Check) { seen[v.Stage] = v.Status }); err == nil || seen["proxy"] != "skipped" || p.active || c.State().Subscription {
		t.Fatal("model-only reassignment failed")
	}
}

func TestUnpairedV1ProfileAndApplyFailure(t *testing.T) {
	c, p := deviceTest(t)
	b, _ := json.Marshal(assigned)
	raw, _ := sealTest(b)
	if err := os.WriteFile(filepath.Join(c.stateDir, "device.bin"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	old, err := NewController(c.home, c.stateDir, p, c.ops)
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
