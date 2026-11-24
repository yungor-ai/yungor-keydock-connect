//go:build windows

package main

import (
	"context"
	"github.com/tailscale/walk"
	d "github.com/tailscale/walk/declarative"
	"github.com/tailscale/win"
	"github.com/yungor-ai/yungor-keydock-connect/internal/desktop"
	"golang.org/x/sys/windows"
	"strings"
)

// This stable GUID also lets Windows retain the user's notification-area preferences.
var trayGUID = windows.GUID{Data1: 0xad3e9258, Data2: 0x1960, Data3: 0x428e, Data4: [8]byte{0xa5, 0x9f, 0x40, 0x7e, 0x2d, 0x94, 0x69, 0xe3}}

type nativeWindow struct {
	app                             *walk.Application
	controller                      *desktop.Controller
	window                          *walk.MainWindow
	server, key                     *walk.LineEdit
	device                          *walk.TextLabel
	message                         *walk.Label
	checks                          *walk.TextEdit
	connect, proxy, launch, restore *walk.PushButton
	state                           desktop.State
	cleaned                         bool
	results                         map[string]desktop.Check
	cancel                          context.CancelFunc
	quitting                        bool
}

func runWindow(dir string, controller *desktop.Controller) error {
	app, err := walk.InitApp()
	if err != nil {
		return err
	}
	ui := &nativeWindow{app: app, controller: controller, results: map[string]desktop.Check{}}
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
	if ui.state.Paired {
		ui.message.SetText("已配对。保留原服务器地址并留空 Device Key 可同步配置。")
	}
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

func (ui *nativeWindow) build(icon *walk.Icon) error {
	err := (d.MainWindow{AssignTo: &ui.window, Title: "KeyDock Connect", Visible: false, Icon: icon, Size: d.Size{Width: 590, Height: 490}, MinSize: d.Size{Width: 490, Height: 435}, Layout: d.VBox{Margins: d.Margins{Left: 18, Top: 28, Right: 18, Bottom: 14}, Spacing: 10}, Children: []d.Widget{
		// TextLabel top-aligns its inner Win32 STATIC across the full row.
		// The compact Label centers a separate, font-measured child HWND.
		d.TextLabel{AssignTo: &ui.device, Text: "设备接入", TextAlignment: d.AlignHNearVNear,
			MinSize: d.Size{Height: 30}, MaxSize: d.Size{Height: 30}, Font: d.Font{Family: "MS Shell Dlg 2", PointSize: 11}},
		d.Composite{Layout: d.Grid{Columns: 2, MarginsZero: true, Spacing: 8}, Children: []d.Widget{
			d.Label{Text: "服务器地址"}, d.LineEdit{AssignTo: &ui.server, Accessibility: d.Accessibility{Name: "服务器地址"}, CueBanner: "https://keydock.example.com"},
			d.Label{Text: "Device Key"}, d.LineEdit{AssignTo: &ui.key, Accessibility: d.Accessibility{Name: "Device Key"}, PasswordMode: true, CueBanner: "请输入管理员签发的 Device Key"},
		}},
		d.Composite{Layout: d.HBox{MarginsZero: true, Spacing: 8}, Children: []d.Widget{
			d.PushButton{AssignTo: &ui.connect, Text: "连接并测试", OnClicked: ui.startConnect},
			d.PushButton{AssignTo: &ui.proxy, Text: "启用系统代理", OnClicked: func() {
				on := !ui.state.Proxy
				ui.runAction("正在设置系统代理…", func(context.Context) error { return ui.controller.SetProxy(on) })
			}},
			d.PushButton{AssignTo: &ui.launch, Text: "打开 Codex", OnClicked: func() {
				ui.runAction("正在打开 Codex…", func(context.Context) error { return ui.controller.OpenCodex() })
			}},
			d.HSpacer{},
		}},
		d.GroupBox{Title: "连接检查", Layout: d.VBox{}, Children: []d.Widget{
			d.TextEdit{AssignTo: &ui.checks, Accessibility: d.Accessibility{Name: "连接检查结果"}, ReadOnly: true, VScroll: true, MinSize: d.Size{Height: 100}},
		}},
		d.Label{AssignTo: &ui.message, Text: "请输入服务器地址与 Device Key。"},
		d.Composite{Layout: d.HBox{MarginsZero: true, Spacing: 8}, Children: []d.Widget{
			d.PushButton{AssignTo: &ui.restore, Text: "恢复原 Codex 配置", OnClicked: func() {
				ui.runAction("正在恢复 Codex 配置…", func(context.Context) error { return ui.controller.Restore() })
			}},
			d.HSpacer{}, d.PushButton{Text: "退出接入助手", OnClicked: ui.quit},
		}},
	}}).Create()
	if err != nil {
		return err
	}
	// Walk creates a native toolbar even with no actions. Detach and dispose
	// of it so no invisible child can participate in printing or cover the header.
	toolbar := ui.window.ToolBar()
	ui.window.SetToolBar(nil)
	toolbar.Dispose()
	return nil
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
	if ui.state.Device != "" {
		ui.device.SetText(ui.state.Device + " · " + ui.state.Model)
	} else {
		ui.device.SetText("设备接入")
	}
	if ui.state.Proxy {
		ui.proxy.SetText("关闭系统代理")
	} else {
		ui.proxy.SetText("启用系统代理")
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

var stages = []struct{ id, label string }{{"device", "设备授权"}, {"codex", "Codex 配置"}, {"model", "模型连通"}, {"proxy", "代理连通"}}

func (ui *nativeWindow) renderChecks() {
	var lines []string
	labels := map[string]string{"running": "进行中", "ok": "完成", "failed": "失败", "skipped": "跳过"}
	for _, s := range stages {
		if result, ok := ui.results[s.id]; ok {
			lines = append(lines, s.label+" · "+labels[result.Status]+"    "+result.Message)
		} else {
			lines = append(lines, s.label+" · 待检查")
		}
	}
	ui.checks.SetText(strings.Join(lines, "\r\n"))
}

func (ui *nativeWindow) startConnect() {
	server, key := strings.TrimSpace(ui.server.Text()), ui.key.Text()
	ui.key.SetText("")
	ui.results = map[string]desktop.Check{}
	ui.renderChecks()
	ui.runAction("正在检查连接…", func(ctx context.Context) error {
		return ui.controller.Connect(ctx, server, key, func(result desktop.Check) {
			ui.app.Synchronize(func() { ui.results[result.Stage] = result; ui.renderChecks() })
		})
	})
}

func (ui *nativeWindow) runAction(progress string, action func(context.Context) error) {
	if ui.cancel != nil || ui.quitting {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ui.cancel = cancel
	ui.enable()
	ui.message.SetText(progress)
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
				ui.message.SetText(err.Error())
			} else {
				ui.message.SetText("操作已完成。")
			}
		})
	}()
}

func (ui *nativeWindow) quit() {
	if ui.quitting {
		return
	}
	ui.quitting = true
	ui.key.SetText("")
	ui.enable()
	ui.message.SetText("正在结束操作并恢复系统代理…")
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
				ui.message.SetText("退出前无法恢复系统代理：" + err.Error())
				return
			}
			ui.cleaned = true
			ui.window.Dispose()
			ui.app.Exit(0)
		})
	}()
}
