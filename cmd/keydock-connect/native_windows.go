//go:build windows

package main

import (
	"context"
	"strings"

	"github.com/tailscale/walk"
	"github.com/tailscale/win"
	"github.com/yungor-ai/yungor-keydock-connect/internal/desktop"
	"golang.org/x/sys/windows"
)

// This stable GUID also lets Windows retain the user's notification-area preferences.
var trayGUID = windows.GUID{Data1: 0xad3e9258, Data2: 0x1960, Data3: 0x428e, Data4: [8]byte{0xa5, 0x9f, 0x40, 0x7e, 0x2d, 0x94, 0x69, 0xe3}}

type nativeWindow struct {
	app                                    *walk.Application
	controller                             *desktop.Controller
	window                                 *walk.MainWindow
	server, key                            *walk.LineEdit
	device, message, proxyState, proxyHint *walk.Label
	rows                                   []checkRow
	colors                                 windowColors
	connect, proxy, launch, restore        *walk.PushButton
	state                                  desktop.State
	cleaned                                bool
	results                                map[string]desktop.Check
	cancel                                 context.CancelFunc
	quitting                               bool
}

func runWindow(dir string, controller *desktop.Controller) error {
	app, err := walk.InitApp()
	if err != nil {
		return err
	}
	ui := &nativeWindow{app: app, controller: controller, results: map[string]desktop.Check{}, colors: nativeColors()}
	icon, err := walk.NewIconFromResourceId(2)
	if err != nil {
		return err
	}
	defer icon.Dispose()
	if err := ui.build(icon); err != nil {
		return err
	}
	// The Walk layout worker closes its quit channel when App.Exit cancels
	// the app context. A form may only be disposed while that worker is alive.
	enteredLoop := false
	defer func() {
		if !enteredLoop && !ui.window.IsDisposed() {
			ui.window.Dispose()
		}
	}()
	ui.window.SetExitOnClose(false)
	tray, err := walk.NewNotifyIconWithGUID(trayGUID)
	if err != nil {
		return err
	}
	defer tray.Dispose()
	if err = tray.SetIcon(icon); err != nil {
		return err
	}
	if err = tray.SetToolTip("KeyDock Connect"); err != nil {
		return err
	}
	addAction := func(label string, fn func()) error {
		action := walk.NewAction()
		if err := action.SetText(label); err != nil {
			return err
		}
		action.Triggered().Attach(fn)
		return tray.ContextMenu().Actions().Add(action)
	}
	if err = addAction("打开 KeyDock Connect", ui.show); err != nil {
		return err
	}
	if err = addAction("退出接入助手", ui.quit); err != nil {
		return err
	}
	tray.MouseUp().Attach(func(_, _ int, b walk.MouseButton) {
		if b == walk.LeftButton {
			ui.show()
		}
	})
	if err = tray.SetVisible(true); err != nil {
		return err
	}
	cleanup, err := watchActivation(dir, func() { app.Synchronize(ui.show) })
	if err != nil {
		return err
	}
	defer cleanup()
	ui.window.Closing().Attach(func(canceled *bool, _ walk.CloseReason) { *canceled = true; ui.key.SetText(""); ui.window.Hide() })
	ui.refresh()
	ui.server.SetText(ui.state.ServerURL)
	ui.renderChecks()
	ui.window.Show() // Starts the form exactly once, after all handlers are ready.
	enteredLoop = true
	app.Run()
	if ui.cancel != nil {
		ui.cancel()
	}
	if ui.cleaned {
		return nil
	}
	return controller.Close() // Includes session shutdown initiated by Windows.
}

func (ui *nativeWindow) show() {
	if ui.quitting {
		return
	}
	ui.window.SetVisible(true)
	h := ui.window.Handle()
	if win.IsIconic(h) {
		win.ShowWindow(h, win.SW_RESTORE)
	}
	win.SetForegroundWindow(h)
	ui.window.Activate()
}

func (ui *nativeWindow) refresh() {
	ui.state = ui.controller.State()
	if ui.state.Paired {
		ui.key.SetCueBanner("使用原服务器时可留空")
	}
	if ui.state.Device != "" {
		ui.device.SetText(ui.state.Device + " · " + ui.state.Model)
	} else {
		ui.device.SetText("输入管理员提供的服务器地址和设备密钥。")
	}
	if ui.state.Proxy {
		ui.proxy.SetText("关闭系统代理")
		ui.proxyState.SetText("系统代理已启用")
		ui.proxyState.SetTextColor(ui.colors.success)
		ui.proxyHint.SetText("使用系统代理的应用流量正按订阅规则分流。")
	} else {
		ui.proxy.SetText("启用系统代理")
		ui.proxyState.SetText("系统代理未启用")
		ui.proxyState.SetTextColor(ui.colors.ink)
		switch {
		case ui.state.Subscription:
			ui.proxyHint.SetText("启用后，使用系统代理的应用流量将按订阅规则分流。")
		case ui.state.Paired:
			ui.proxyHint.SetText("当前设备未分配 SS 订阅。")
		default:
			ui.proxyHint.SetText("连接设备并获取 SS 订阅后，可在此启用。")
		}
	}
	ui.enable()
}

func (ui *nativeWindow) enable() {
	idle := ui.cancel == nil && !ui.quitting
	ui.server.SetEnabled(idle)
	ui.key.SetEnabled(idle)
	ui.connect.SetEnabled(idle)
	ui.launch.SetEnabled(idle && ui.state.Configured)
	ui.restore.SetEnabled(idle && ui.state.Restorable)
	ui.proxy.SetEnabled(idle && (ui.state.Subscription || ui.state.Proxy))
}

func (ui *nativeWindow) startConnect() {
	server, key := strings.TrimSpace(ui.server.Text()), ui.key.Text()
	ui.key.SetText("")
	ui.results = map[string]desktop.Check{}
	ui.renderChecks()
	ui.runAction("正在检查连接…", "连接检查已完成", func(ctx context.Context) error {
		return ui.controller.Connect(ctx, server, key, func(result desktop.Check) {
			ui.app.Synchronize(func() { ui.results[result.Stage] = result; ui.renderChecks() })
		})
	})
}

func (ui *nativeWindow) runAction(progress, done string, action func(context.Context) error) {
	if ui.cancel != nil || ui.quitting {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ui.cancel = cancel
	ui.enable()
	ui.setMessage(progress, ui.colors.accent)
	go func() {
		err := action(ctx)
		cancel()
		ui.app.Synchronize(func() {
			ui.cancel = nil
			if ui.quitting {
				ui.finishQuit()
				return
			}
			ui.refresh()
			if err != nil {
				ui.setMessage(err.Error(), ui.colors.danger)
			} else {
				ui.setMessage(done, ui.colors.success)
			}
		})
	}()
}

func (ui *nativeWindow) setMessage(text string, color walk.Color) {
	ui.message.SetTextColor(color)
	ui.message.SetText(strings.ReplaceAll(text, "\n", "　"))
	ui.message.SetToolTipText(text)
}

func (ui *nativeWindow) quit() {
	if ui.quitting {
		return
	}
	ui.quitting = true
	ui.key.SetText("")
	ui.enable()
	ui.setMessage("正在结束操作并恢复系统代理…", ui.colors.accent)
	if ui.cancel != nil {
		ui.cancel()
		return
	}
	ui.finishQuit()
}

func (ui *nativeWindow) finishQuit() {
	go func() {
		err := ui.controller.Close()
		ui.app.Synchronize(func() {
			if err != nil {
				ui.quitting = false
				ui.refresh()
				ui.show()
				ui.setMessage("退出前无法恢复系统代理："+err.Error(), ui.colors.danger)
				return
			}
			ui.cleaned = true
			ui.window.Dispose()
			ui.app.Exit(0)
		})
	}()
}
