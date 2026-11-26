//go:build windows

package main

import (
	"context"
	"errors"
	"strings"
	"time"
	"unsafe"

	"github.com/tailscale/walk"
	"github.com/tailscale/win"
	"github.com/yungor-ai/yungor-keydock-connect/internal/desktop"
	"golang.org/x/sys/windows"
)

const exportDiagnosticsLabel = "导出诊断日志"

// This stable GUID also lets Windows retain the user's notification-area preferences.
var trayGUID = windows.GUID{Data1: 0xad3e9258, Data2: 0x1960, Data3: 0x428e, Data4: [8]byte{0xa5, 0x9f, 0x40, 0x7e, 0x2d, 0x94, 0x69, 0xe3}}

type nativeWindow struct {
	app                        *walk.Application
	controller                 *desktop.Controller
	window                     *walk.MainWindow
	server, key                *walk.LineEdit
	device, message, proxyHint *walk.TextLabel
	clockHint                  *walk.TextLabel
	troubleshooting            *walk.Composite
	proxyState, proxyUpdatedAt *walk.Label
	proxyModes                 *walk.ComboBox
	rows                       []checkRow
	colors                     windowColors
	connect, proxy, restore    *walk.PushButton
	export, timeSettings       *walk.PushButton
	downloads, checkUpdates    *walk.PushButton
	state                      desktop.State
	cleaned                    bool
	results                    map[string]desktop.Check
	cancel                     context.CancelFunc
	quitting                   bool
	choosingExportPath         bool
	confirmingUpdate           bool
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
	ui.proxyModes.CurrentIndexChanged().Attach(ui.renderProxy)
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
	lifetime, stopBackground := context.WithCancel(context.Background())
	go controller.RefreshSubscriptions(lifetime, func() {
		if lifetime.Err() != nil {
			return
		}
		app.Synchronize(func() {
			if lifetime.Err() == nil && !ui.quitting && ui.cancel == nil {
				ui.refresh()
			}
		})
	})
	go controller.WatchNetwork(lifetime, func(result desktop.Check) {
		if lifetime.Err() != nil {
			return
		}
		app.Synchronize(func() {
			if lifetime.Err() == nil && !ui.quitting {
				ui.results[result.Stage] = result
				ui.renderChecks()
			}
		})
	})
	app.Run()
	stopBackground()
	if ui.cancel != nil {
		ui.cancel()
	}
	if ui.cleaned {
		return nil
	}
	return controller.Close() // Includes session shutdown initiated by Windows.
}

func (ui *nativeWindow) show() {
	if ui.quitting || ui.choosingExportPath || ui.confirmingUpdate {
		return
	}
	if ui.cancel == nil {
		ui.refresh()
	}
	if !ui.window.Visible() {
		ui.window.SetVisible(true)
		ui.window.RequestLayout()
	}
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
	ui.enable()
}

func (ui *nativeWindow) enable() {
	idle := ui.cancel == nil && !ui.quitting
	ui.server.SetEnabled(idle)
	ui.key.SetEnabled(idle)
	ui.connect.SetEnabled(idle)
	ui.restore.SetEnabled(idle && ui.state.Restorable)
	ui.export.SetEnabled(idle)
	ui.timeSettings.SetEnabled(idle)
	ui.downloads.SetEnabled(idle)
	ui.checkUpdates.SetEnabled(idle)
	ui.renderProxy()
}

func (ui *nativeWindow) openTimeSettings() {
	if windows.GetCurrentProcessToken().IsElevated() {
		ui.setMessage("请从 Windows 开始菜单打开设置，进入「时间和语言」→「日期和时间」。", ui.colors.accent)
		return
	}
	settings := windows.StringToUTF16Ptr("ms-settings:dateandtime")
	if err := windows.ShellExecute(windows.Handle(ui.window.Handle()), nil, settings, nil, nil, win.SW_SHOWNORMAL); err != nil {
		ui.setMessage("无法打开时间设置。请从 Windows 设置中进入「时间和语言」→「日期和时间」。", ui.colors.danger)
	}
}

func (ui *nativeWindow) exportDiagnostics() {
	if ui.cancel != nil || ui.quitting || ui.choosingExportPath {
		return
	}

	// Walk's file dialog sets OFN_FILEMUSTEXIST even in save mode. Use the
	// common dialog directly to allow a new file and confirm replacement.
	var filename [4096]uint16
	suggested := "KeyDockConnect-diagnostics-" + time.Now().Format("20060102-150405") + ".json"
	copy(filename[:], windows.StringToUTF16(suggested))
	filter := append(windows.StringToUTF16("JSON 文件 (*.json)"), windows.StringToUTF16("*.json")...)
	filter = append(filter, 0)
	dlg := win.OPENFILENAME{
		HwndOwner:    ui.window.Handle(),
		LpstrFilter:  &filter[0],
		NFilterIndex: 1,
		LpstrFile:    &filename[0],
		NMaxFile:     uint32(len(filename)),
		LpstrTitle:   windows.StringToUTF16Ptr(exportDiagnosticsLabel),
		Flags:        win.OFN_EXPLORER | win.OFN_HIDEREADONLY | win.OFN_NOCHANGEDIR | win.OFN_OVERWRITEPROMPT | win.OFN_PATHMUSTEXIST,
	}
	dlg.LStructSize = uint32(unsafe.Sizeof(dlg))
	ui.choosingExportPath = true
	accepted := win.GetSaveFileName(&dlg)
	ui.choosingExportPath = false
	if !accepted {
		if win.CommDlgExtendedError() != 0 {
			ui.setMessage("无法打开诊断日志保存窗口", ui.colors.danger)
		}
		return
	}
	path := windows.UTF16ToString(filename[:])
	ui.runAction("正在保存诊断日志…", "诊断日志已保存，可发送给管理员。", func(context.Context) error {
		if err := ui.controller.ExportDiagnostics(path); err != nil {
			return errors.New("无法保存诊断日志，请选择可写入的位置后重试")
		}
		return nil
	})
}

func (ui *nativeWindow) startConnect() {
	server, key := strings.TrimSpace(ui.server.Text()), ui.key.Text()
	ui.key.SetText("")
	for _, stage := range connectionStages {
		delete(ui.results, stage.id)
	}
	ui.renderChecks()
	ui.runAction("正在检查连接…", "连接检查已完成", func(ctx context.Context) error {
		return ui.controller.Connect(ctx, server, key, func(result desktop.Check) {
			ui.app.Synchronize(func() { ui.results[result.Stage] = result; ui.renderChecks() })
		})
	})
}

func (ui *nativeWindow) runAction(progress, done string, action func(context.Context) error) {
	ui.runTask(progress, action, func(err error) {
		if err != nil {
			ui.setMessage(err.Error(), ui.colors.danger)
		} else {
			ui.setMessage(done, ui.colors.success)
		}
	})
}

func (ui *nativeWindow) runTask(progress string, action func(context.Context) error, completed func(error)) {
	if ui.cancel != nil || ui.quitting || ui.choosingExportPath || ui.confirmingUpdate {
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
			completed(err)
		})
	}()
}

func (ui *nativeWindow) setMessage(text string, color walk.Color) {
	ui.message.SetTextColor(color)
	ui.message.SetText(strings.ReplaceAll(text, "\n", "　"))
	ui.message.SetToolTipText(text)
}

func (ui *nativeWindow) quit() {
	if ui.quitting || ui.choosingExportPath || ui.confirmingUpdate {
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
			ui.exitAfterCleanup()
		})
	}()
}

func (ui *nativeWindow) exitAfterCleanup() {
	ui.cleaned = true
	ui.window.Dispose()
	ui.app.Exit(0)
}
