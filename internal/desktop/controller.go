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
	"time"

	"github.com/yungor-ai/yungor-keydock-connect/internal/codex"
	"github.com/yungor-ai/yungor-keydock-connect/internal/connect"
	"github.com/yungor-ai/yungor-keydock-connect/internal/diagnostics"
	"github.com/yungor-ai/yungor-keydock-connect/internal/proxy"
)

type Proxy interface {
	StartMode(string, proxy.Mode) error
	CheckMode(proxy.Mode) error
	Stop() error
	ActiveMode() proxy.Mode
	Test(context.Context, string) error
	Refresh(context.Context, string) error
}

type Backend struct {
	Apply              func(home, stateDir, base, model, key string) error
	Restore            func(home, stateDir string) error
	Configured         func(home, stateDir string) bool
	Matches            func(home, stateDir, base, model, key string) bool
	Protect, Unprotect func([]byte) ([]byte, error)
	Launch             func() error
	Fetch              func(context.Context, string, string) (connect.DeviceConfig, error)
	TestModel          func(context.Context, connect.Profile) error
}

func SystemBackend() Backend {
	client := connect.Client{}
	return Backend{Apply: codex.Apply, Restore: codex.Restore, Configured: codex.Configured, Matches: codex.Matches, Protect: codex.Protect, Unprotect: codex.Unprotect, Launch: launchCodex, Fetch: client.Fetch, TestModel: client.TestModel}
}

// The embedded fields retain the encrypted device.bin layout used in v0.1.
type enrollment struct {
	connect.Profile
	ServerURL             string    `json:"server_url,omitempty"`
	DeviceKey             string    `json:"device_key,omitempty"`
	SubscriptionUpdatedAt time.Time `json:"subscription_updated_at,omitzero"`
}

type Controller struct {
	operation      sync.Mutex
	mu             sync.RWMutex
	device         enrollment
	proxy          Proxy
	ops            Backend
	home, stateDir string
	diagnostics    *diagnostics.Log
	checkedAt      time.Time
	refreshFailed  bool
}

type State struct {
	ServerURL, Device, Model, Base                      string
	Paired, Configured, Restorable, Proxy, Subscription bool
	ProxyMode                                           proxy.Mode
	SubscriptionCheckedAt                               time.Time
	SubscriptionUpdatedAt                               time.Time
	SubscriptionRefreshFailed                           bool
}

type Check struct{ Stage, Status, Message, Hint string }

func NewController(home, stateDir string, proxy Proxy, ops Backend, log *diagnostics.Log) (*Controller, error) {
	c := &Controller{home: home, stateDir: stateDir, proxy: proxy, ops: ops, diagnostics: log}
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
	c.mu.RLock()
	p, checkedAt, refreshFailed := c.device, c.checkedAt, c.refreshFailed
	c.mu.RUnlock()
	mode := c.proxy.ActiveMode()
	return State{ServerURL: p.ServerURL, Device: p.DeviceName, Model: p.Model, Base: p.BaseURL, Paired: p.DeviceKey != "" && p.ServerURL != "",
		Configured: p.Key != "" && c.ops.Matches(c.home, c.stateDir, p.BaseURL, p.Model, p.Key),
		Restorable: c.ops.Configured(c.home, c.stateDir), Proxy: mode != "", Subscription: p.SubscriptionURL != "",
		ProxyMode: mode, SubscriptionCheckedAt: checkedAt, SubscriptionUpdatedAt: p.SubscriptionUpdatedAt, SubscriptionRefreshFailed: refreshFailed}
}

func (c *Controller) ExportDiagnostics(path string) error {
	state := c.State()
	return c.diagnostics.Export(path, diagnostics.State{Paired: state.Paired, CodexConfigured: state.Configured,
		SystemProxyActive: state.ProxyMode == proxy.ModeSystem, ProxyMode: string(state.ProxyMode), SubscriptionAssigned: state.Subscription})
}

func (c *Controller) persist(p enrollment) error {
	if err := c.saveEnrollment(p); err != nil {
		return err
	}
	c.mu.Lock()
	if c.device.SubscriptionURL != p.SubscriptionURL {
		c.checkedAt, c.refreshFailed = time.Time{}, false
	}
	c.device = p
	c.mu.Unlock()
	return nil
}

func (c *Controller) saveEnrollment(p enrollment) error {
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
	var stageStart time.Time
	var clockSkew time.Duration
	check := func(stage, status, message string, err error) {
		var elapsed time.Duration
		outcome := status
		switch status {
		case "running":
			stageStart = time.Now()
			outcome = "start"
		case "ok", "failed":
			elapsed = time.Since(stageStart)
		}
		c.diagnostics.Record(stage, outcome, elapsed, err, 0)
		if report != nil {
			result := Check{Stage: stage, Status: status, Message: message}
			if stage == "proxy" && status == "failed" && !errors.Is(err, context.Canceled) {
				result.Hint = proxyClockHint(clockSkew)
			}
			report(result)
		}
	}
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	deviceKey = strings.TrimSpace(deviceKey)
	prev := c.snapshot()
	check("device", "running", "正在向 KeyDock 获取设备配置…", nil)
	stepError := func(stage string, err error) error {
		check(stage, "failed", err.Error(), err)
		return err
	}
	if deviceKey == "" {
		// Never send a cached device credential to a different origin or base path.
		if serverURL == "" || serverURL != prev.ServerURL || prev.DeviceKey == "" {
			return stepError("device", errors.New("请输入该服务地址对应的设备接入密钥"))
		}
		deviceKey = prev.DeviceKey
	}
	device, err := c.ops.Fetch(ctx, serverURL, deviceKey)
	if err != nil {
		return stepError("device", err)
	}
	if err = ctx.Err(); err != nil {
		return stepError("device", err)
	}
	p := device.Profile
	clockSkew = device.ClockSkew
	// An active worker using another subscription must not outlive the device switch.
	switched := c.proxy.ActiveMode() != "" && prev.SubscriptionURL != p.SubscriptionURL
	if switched {
		if err = c.proxy.Stop(); err != nil {
			return stepError("device", err)
		}
	}
	next := enrollment{Profile: p, ServerURL: serverURL, DeviceKey: deviceKey}
	if next.SubscriptionURL != "" && next.SubscriptionURL == prev.SubscriptionURL {
		next.SubscriptionUpdatedAt = prev.SubscriptionUpdatedAt
	}
	if next != prev {
		if err = c.persist(next); err != nil {
			return stepError("device", err)
		}
	}
	message := "已获取 " + p.DeviceName + " 的配置"
	if switched {
		message += "，原设备的代理已关闭"
	}
	check("device", "ok", message, nil)
	check("codex", "running", "正在更新 Codex 配置…", nil)
	err = c.ops.Apply(c.home, c.stateDir, p.BaseURL, p.Model, p.Key)
	if err == nil && !c.ops.Matches(c.home, c.stateDir, p.BaseURL, p.Model, p.Key) {
		err = errors.New("已执行配置写入，但当前 Codex 配置与服务端不一致")
	}
	var problems []error
	if err != nil {
		problems = append(problems, stepError("codex", err))
		check("model", "skipped", "Codex 配置未就绪，已跳过模型请求", nil)
	} else {
		check("codex", "ok", "已配置模型 "+p.Model, nil)
		check("model", "running", "正在发送一条简短的模型请求…", nil)
		if err = c.ops.TestModel(ctx, p); err != nil {
			problems = append(problems, stepError("model", err))
		} else {
			check("model", "ok", "已收到模型回复", nil)
		}
	}
	switch {
	case p.SubscriptionURL == "":
		check("proxy", "skipped", "该设备未分配 SS 订阅", nil)
	case ctx.Err() != nil:
		check("proxy", "skipped", "已取消代理测试", ctx.Err())
		problems = append(problems, ctx.Err())
	default:
		check("proxy", "running", "正在通过设备的 SS 节点测试连接…", nil)
		if err = c.proxy.Test(ctx, p.SubscriptionURL); err != nil {
			problems = append(problems, stepError("proxy", err))
		} else {
			check("proxy", "ok", "已通过 SS 节点完成 HTTPS 请求", nil)
		}
	}
	return errors.Join(problems...)
}

func proxyClockHint(skew time.Duration) string {
	const syncWindowsTime = "请在 Windows 时间设置中点「立即同步」，再复测。"
	if skew == 0 {
		return syncWindowsTime + "SS2022 会拒绝两端时钟相差超过 30 秒的连接。"
	}
	direction := "快"
	if skew < 0 {
		direction = "慢"
	}
	return "检测到本机时钟比 KeyDock 服务端" + direction + "了 30 秒以上。" + syncWindowsTime
}

func (c *Controller) CheckProxyMode(mode proxy.Mode) error { return c.proxy.CheckMode(mode) }

func (c *Controller) SetProxyMode(on bool, mode proxy.Mode) error {
	if !c.operation.TryLock() {
		return errBusy
	}
	defer c.operation.Unlock()
	if !on {
		return c.recordAction("proxy.disable", func() error {
			if err := c.proxy.Stop(); err != nil {
				return err
			}
			c.mu.Lock()
			c.checkedAt, c.refreshFailed = time.Time{}, false
			c.mu.Unlock()
			return nil
		})
	}
	return c.recordAction("proxy.enable", func() error {
		url := c.snapshot().SubscriptionURL
		if url == "" {
			return errors.New("该设备没有可用的 SS 订阅")
		}
		if active := c.proxy.ActiveMode(); active != "" && active == mode {
			return nil
		}
		if err := c.proxy.StartMode(url, mode); err != nil {
			return err
		}
		c.noteSubscriptionRefresh(false)
		return nil
	})
}

// RefreshSubscriptions keeps an active desktop or tray proxy current. Starting
// either mode fetches the subscription immediately, so idle devices need no poll.
func (c *Controller) RefreshSubscriptions(ctx context.Context, onRefresh func()) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c.refreshActiveSubscription(ctx) && onRefresh != nil && ctx.Err() == nil {
				onRefresh()
			}
		}
	}
}

func (c *Controller) refreshActiveSubscription(ctx context.Context) bool {
	if !c.operation.TryLock() {
		return false
	}
	defer c.operation.Unlock()
	url := c.snapshot().SubscriptionURL
	if ctx.Err() != nil || url == "" || c.proxy.ActiveMode() == "" {
		return false
	}
	err := c.recordAction("proxy.subscription_refresh", func() error { return c.proxy.Refresh(ctx, url) })
	c.noteSubscriptionRefresh(err != nil)
	return true
}

func (c *Controller) noteSubscriptionRefresh(failed bool) {
	now := time.Now()
	if !failed {
		p := c.snapshot()
		p.SubscriptionUpdatedAt = now.UTC()
		if err := c.saveEnrollment(p); err != nil {
			c.diagnostics.RecordResult("proxy.subscription_status", now, err)
		}
	}
	c.mu.Lock()
	c.checkedAt, c.refreshFailed = now, failed
	if !failed {
		c.device.SubscriptionUpdatedAt = now.UTC()
	}
	c.mu.Unlock()
}

func (c *Controller) Restore() error {
	if !c.operation.TryLock() {
		return errBusy
	}
	defer c.operation.Unlock()
	return c.recordAction("codex.restore", func() error { return c.ops.Restore(c.home, c.stateDir) })
}
func (c *Controller) OpenCodex() error { return c.recordAction("codex.launch", c.ops.Launch) }

// The shell cancels any in-progress Connect before calling Close. Wait until it
// releases the operation so a racing proxy toggle cannot run after shutdown.
func (c *Controller) Close() error {
	c.operation.Lock()
	defer c.operation.Unlock()
	return c.recordAction("app.shutdown", c.proxy.Stop)
}

func (c *Controller) recordAction(stage string, action func() error) error {
	start := time.Now()
	err := action()
	c.diagnostics.RecordResult(stage, start, err)
	return err
}
