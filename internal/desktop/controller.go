// Package desktop coordinates the employee's native setup window and tray actions.
package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yungor-ai/yungor-keydock-connect/internal/codex"
	"github.com/yungor-ai/yungor-keydock-connect/internal/connect"
)

type Proxy interface {
	Start(string) error
	Stop() error
	Active() bool
	Test(context.Context, string) error
}

type Backend struct {
	Apply              func(home, stateDir, base, model, key string) error
	Restore            func(home, stateDir string) error
	Configured         func(home, stateDir string) bool
	Matches            func(home, stateDir, base, model, key string) bool
	Protect, Unprotect func([]byte) ([]byte, error)
	Launch             func() error
	Fetch              func(context.Context, string, string) (connect.Profile, error)
	TestModel          func(context.Context, connect.Profile) error
}

func SystemBackend() Backend {
	client := connect.Client{}
	return Backend{Apply: codex.Apply, Restore: codex.Restore, Configured: codex.Configured, Matches: codex.Matches, Protect: codex.Protect, Unprotect: codex.Unprotect, Launch: launchCodex, Fetch: client.Fetch, TestModel: client.TestModel}
}

// The embedded fields retain the encrypted device.bin layout used in v0.1.
type enrollment struct {
	connect.Profile
	ServerURL string `json:"server_url,omitempty"`
	DeviceKey string `json:"device_key,omitempty"`
}

type Controller struct {
	operation      sync.Mutex
	mu             sync.RWMutex
	device         enrollment
	proxy          Proxy
	ops            Backend
	home, stateDir string
}

type State struct {
	ServerURL, Device, Model, Base                      string
	Paired, Configured, Restorable, Proxy, Subscription bool
}

type Check struct{ Stage, Status, Message string }

func NewController(home, stateDir string, proxy Proxy, ops Backend) (*Controller, error) {
	c := &Controller{home: home, stateDir: stateDir, proxy: proxy, ops: ops}
	raw, err := os.ReadFile(c.devicePath())
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	raw, err = ops.Unprotect(raw)
	if err != nil || json.Unmarshal(raw, &c.device) != nil || c.device.Key == "" {
		return nil, errors.New("无法读取此 Windows 用户的设备资料")
	}
	return c, nil
}

func (c *Controller) devicePath() string { return filepath.Join(c.stateDir, "device.bin") }

func (c *Controller) snapshot() enrollment { c.mu.RLock(); defer c.mu.RUnlock(); return c.device }

func (c *Controller) State() State {
	p := c.snapshot()
	return State{ServerURL: p.ServerURL, Device: p.DeviceName, Model: p.Model, Base: p.BaseURL, Paired: p.DeviceKey != "" && p.ServerURL != "",
		Configured: p.Key != "" && c.ops.Matches(c.home, c.stateDir, p.BaseURL, p.Model, p.Key),
		Restorable: c.ops.Configured(c.home, c.stateDir), Proxy: c.proxy.Active(), Subscription: p.SubscriptionURL != ""}
}

func (c *Controller) persist(p enrollment) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	ciphertext, err := c.ops.Protect(raw)
	if err != nil {
		return errors.New("无法加密设备资料")
	}
	if err = writePrivate(c.devicePath(), ciphertext); err != nil {
		return errors.New("无法保存本机设备资料")
	}
	c.mu.Lock()
	c.device = p
	c.mu.Unlock()
	return nil
}

func writePrivate(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".keydock-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

var errBusy = errors.New("接入助手正在处理其他操作，请稍后重试")

// Connect saves the device-scoped reply, applies it, and checks actual model and
// proxy traffic. Each step is reported independently; a failed model test does
// not suppress the SS test. Only SetProxy enables the Windows system proxy.
func (c *Controller) Connect(ctx context.Context, serverURL, deviceKey string, report func(Check)) error {
	if !c.operation.TryLock() {
		return errBusy
	}
	defer c.operation.Unlock()
	if report == nil {
		report = func(Check) {}
	}
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	deviceKey = strings.TrimSpace(deviceKey)
	prev := c.snapshot()
	report(Check{"device", "running", "正在向 KeyDock 获取设备配置…"})
	stepError := func(stage string, err error) error { report(Check{stage, "failed", err.Error()}); return err }
	if deviceKey == "" {
		// Never send a cached device credential to a different origin or base path.
		if serverURL == "" || serverURL != prev.ServerURL || prev.DeviceKey == "" {
			return stepError("device", errors.New("请输入该服务地址对应的设备接入密钥"))
		}
		deviceKey = prev.DeviceKey
	}
	p, err := c.ops.Fetch(ctx, serverURL, deviceKey)
	if err != nil {
		return stepError("device", err)
	}
	if err = ctx.Err(); err != nil {
		return stepError("device", err)
	}
	// An active worker using another subscription must not outlive the device switch.
	switched := c.proxy.Active() && prev.SubscriptionURL != p.SubscriptionURL
	if switched {
		if err = c.proxy.Stop(); err != nil {
			return stepError("device", err)
		}
	}
	next := enrollment{Profile: p, ServerURL: serverURL, DeviceKey: deviceKey}
	if next != prev {
		if err = c.persist(next); err != nil {
			return stepError("device", err)
		}
	}
	message := "已获取 " + p.DeviceName + " 的配置"
	if switched {
		message += "，原设备的系统代理已关闭"
	}
	report(Check{"device", "ok", message})
	report(Check{"codex", "running", "正在更新 Codex 配置…"})
	err = c.ops.Apply(c.home, c.stateDir, p.BaseURL, p.Model, p.Key)
	if err == nil && !c.ops.Matches(c.home, c.stateDir, p.BaseURL, p.Model, p.Key) {
		err = errors.New("已执行配置写入，但当前 Codex 配置与服务端不一致")
	}
	var problems []error
	if err != nil {
		problems = append(problems, stepError("codex", err))
		report(Check{"model", "skipped", "Codex 配置未就绪，已跳过模型请求"})
	} else {
		report(Check{"codex", "ok", "已配置模型 " + p.Model})
		report(Check{"model", "running", "正在发送一条简短的模型请求…"})
		if err = c.ops.TestModel(ctx, p); err != nil {
			problems = append(problems, stepError("model", err))
		} else {
			report(Check{"model", "ok", "已收到模型回复"})
		}
	}
	switch {
	case p.SubscriptionURL == "":
		report(Check{"proxy", "skipped", "该设备未分配 SS 订阅"})
	case ctx.Err() != nil:
		report(Check{"proxy", "skipped", "已取消代理测试"})
		problems = append(problems, ctx.Err())
	default:
		report(Check{"proxy", "running", "正在通过设备的 SS 节点测试连接…"})
		if err = c.proxy.Test(ctx, p.SubscriptionURL); err != nil {
			problems = append(problems, stepError("proxy", err))
		} else {
			report(Check{"proxy", "ok", "已通过 SS 节点完成 HTTPS 请求"})
		}
	}
	return errors.Join(problems...)
}

func (c *Controller) SetProxy(on bool) error {
	if !c.operation.TryLock() {
		return errBusy
	}
	defer c.operation.Unlock()
	if !on {
		return c.proxy.Stop()
	}
	url := c.snapshot().SubscriptionURL
	if url == "" {
		return errors.New("该设备没有可用的 SS 订阅")
	}
	return c.proxy.Start(url)
}

func (c *Controller) Restore() error {
	if !c.operation.TryLock() {
		return errBusy
	}
	defer c.operation.Unlock()
	return c.ops.Restore(c.home, c.stateDir)
}
func (c *Controller) OpenCodex() error { return c.ops.Launch() }

// The shell cancels any in-progress Connect before calling Close. Wait until it
// releases the operation so a racing proxy toggle cannot run after shutdown.
func (c *Controller) Close() error {
	c.operation.Lock()
	defer c.operation.Unlock()
	return c.proxy.Stop()
}
