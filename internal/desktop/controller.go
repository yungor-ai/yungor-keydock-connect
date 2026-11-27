// Package desktop coordinates the employee's native setup window and tray actions.
package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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
	Fetch              func(context.Context, string, string) (connect.DeviceConfig, error)
	TestModel          func(context.Context, connect.Profile) error
}

func SystemBackend(stateDir string) Backend {
	client := connect.Client{}
	return Backend{
		Apply: codex.Apply, Restore: codex.Restore, Configured: codex.Configured, Matches: codex.Matches,
		Protect:   func(data []byte) ([]byte, error) { return codex.ProtectForState(stateDir, data) },
		Unprotect: func(data []byte) ([]byte, error) { return codex.UnprotectForState(stateDir, data) },
		Fetch:     client.Fetch, TestModel: client.TestModel,
	}
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
	networkChanged chan struct{}
}

type State struct {
	ServerURL, Device, Model, Base, ProxyAddress        string
	Paired, Configured, Restorable, Proxy, Subscription bool
	ProxyMode                                           proxy.Mode
	SubscriptionCheckedAt                               time.Time
	SubscriptionUpdatedAt                               time.Time
	SubscriptionRefreshFailed                           bool
}

type Check struct{ Stage, Status, Message, Hint string }

func NewController(home, stateDir string, proxy Proxy, ops Backend, log *diagnostics.Log) (*Controller, error) {
	c := &Controller{home: home, stateDir: stateDir, proxy: proxy, ops: ops, diagnostics: log, networkChanged: make(chan struct{}, 1)}
	raw, err := os.ReadFile(c.devicePath())
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	raw, err = ops.Unprotect(raw)
	if err != nil || json.Unmarshal(raw, &c.device) != nil || c.device.Key == "" {
		return nil, errors.New("无法读取当前用户的设备资料")
	}
	return c, nil
}

func (c *Controller) devicePath() string { return filepath.Join(c.stateDir, "device.bin") }

func (c *Controller) snapshot() enrollment { c.mu.RLock(); defer c.mu.RUnlock(); return c.device }

func (c *Controller) ProxyAddress() string {
	if local, ok := c.proxy.(interface{ ProxyAddress() string }); ok {
		return local.ProxyAddress()
	}
	return ""
}

func (c *Controller) ActiveProxyMode() proxy.Mode { return c.proxy.ActiveMode() }

func (c *Controller) State() State {
	c.mu.RLock()
	p, checkedAt, refreshFailed := c.device, c.checkedAt, c.refreshFailed
	c.mu.RUnlock()
	mode := c.ActiveProxyMode()
	return State{ServerURL: p.ServerURL, Device: p.DeviceName, Model: p.Model, Base: p.BaseURL, Paired: p.DeviceKey != "" && p.ServerURL != "",
		Configured: p.Key != "" && c.ops.Matches(c.home, c.stateDir, p.BaseURL, p.Model, p.Key),
		Restorable: c.ops.Configured(c.home, c.stateDir), Proxy: mode != "", Subscription: p.SubscriptionURL != "",
		ProxyMode: mode, ProxyAddress: c.ProxyAddress(), SubscriptionCheckedAt: checkedAt, SubscriptionUpdatedAt: p.SubscriptionUpdatedAt, SubscriptionRefreshFailed: refreshFailed}
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

type accessSetup struct{ serverURL, deviceKey string }

// Connect saves the device-scoped reply, applies it, and checks actual model and
// proxy traffic. Each step is reported independently; a failed model test does
// not suppress the SS test. Only SetProxyMode enables a sustained proxy.
func (c *Controller) Connect(ctx context.Context, serverURL, deviceKey string, report func(Check)) error {
	return c.checkAccess(ctx, &accessSetup{serverURL, deviceKey}, report)
}

// RecheckAccess verifies the saved device authorization and its current assignment.
// Only Connect applies the assignment to the local device and Codex.
func (c *Controller) RecheckAccess(ctx context.Context, report func(Check)) error {
	return c.checkAccess(ctx, nil, report)
}

func (c *Controller) checkAccess(ctx context.Context, setup *accessSetup, report func(Check)) error {
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
	prev := c.snapshot()
	check("device", "running", "正在向 KeyDock 获取设备配置…", nil)
	stepError := func(stage string, err error) error {
		check(stage, "failed", err.Error(), err)
		if stage == "device" && setup == nil {
			for _, dependent := range []string{"codex", "model", "proxy"} {
				check(dependent, "skipped", "设备授权未通过，已跳过", nil)
			}
		}
		return err
	}
	serverURL, deviceKey := prev.ServerURL, prev.DeviceKey
	if setup != nil {
		serverURL = strings.TrimRight(strings.TrimSpace(setup.serverURL), "/")
		deviceKey = strings.TrimSpace(setup.deviceKey)
		if deviceKey == "" && serverURL != "" && serverURL == prev.ServerURL {
			// A saved credential is valid only for its original origin and base path.
			deviceKey = prev.DeviceKey
		}
		if deviceKey == "" {
			return stepError("device", errors.New("请输入该服务地址对应的设备接入密钥"))
		}
	} else if serverURL == "" || deviceKey == "" {
		return stepError("device", errors.New("请先完成设备接入，再重新检查接入"))
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
	message := "已获取 " + p.DeviceName + " 的配置"
	if setup != nil {
		next := enrollment{Profile: p, ServerURL: serverURL, DeviceKey: deviceKey}
		switched, err := c.updateEnrollment(prev, next)
		if err != nil {
			return stepError("device", err)
		}
		if switched {
			message += "，原设备的代理已关闭"
		}
		c.RecheckNetwork()
	}
	check("device", "ok", message, nil)
	if setup != nil {
		check("codex", "running", "正在更新 Codex 配置…", nil)
		err = c.ops.Apply(c.home, c.stateDir, p.BaseURL, p.Model, p.Key)
	} else {
		check("codex", "running", "正在核对本机 Codex 配置…", nil)
	}
	if err == nil && !c.ops.Matches(c.home, c.stateDir, p.BaseURL, p.Model, p.Key) {
		err = errors.New("本机 Codex 配置与服务端不一致，请点击「连接并测试」同步")
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

// An active worker using a different subscription must stop before the device switches.
func (c *Controller) updateEnrollment(prev, next enrollment) (bool, error) {
	switched := c.proxy.ActiveMode() != "" && prev.SubscriptionURL != next.SubscriptionURL
	if switched {
		if err := c.proxy.Stop(); err != nil {
			return false, err
		}
	}
	if next.SubscriptionURL != "" && next.SubscriptionURL == prev.SubscriptionURL {
		next.SubscriptionUpdatedAt = prev.SubscriptionUpdatedAt
	}
	if next != prev {
		if err := c.persist(next); err != nil {
			return switched, err
		}
	}
	return switched, nil
}

func proxyClockHint(skew time.Duration) string {
	syncTime := "请在 Windows 时间设置中点「立即同步」，再复测。"
	if runtime.GOOS == "darwin" {
		syncTime = "请在系统设置的「通用 → 日期与时间」中开启自动设置日期与时间，再复测。"
	}
	if skew == 0 {
		return syncTime + "SS2022 会拒绝两端时钟相差超过 30 秒的连接。"
	}
	direction := "快"
	if skew < 0 {
		direction = "慢"
	}
	return "检测到本机时钟比 KeyDock 服务端" + direction + "了 30 秒以上。" + syncTime
}

func (c *Controller) SetProxyMode(on bool, mode proxy.Mode) error {
	if !c.operation.TryLock() {
		return errBusy
	}
	defer c.operation.Unlock()
	defer c.RecheckNetwork()
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
		active := c.proxy.ActiveMode()
		if active != "" {
			if active == mode {
				return nil
			}
			if err := c.proxy.CheckMode(mode); err != nil {
				return err
			}
			if err := c.proxy.Stop(); err != nil {
				return err
			}
		}
		if err := c.proxy.StartMode(url, mode); err != nil {
			if active != "" {
				if restoreErr := c.proxy.StartMode(url, active); restoreErr != nil {
					return errors.Join(err, fmt.Errorf("无法恢复原代理：%w", restoreErr))
				}
			}
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
	c.RecheckNetwork()
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
