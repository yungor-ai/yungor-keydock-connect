//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/tailscale/walk"
	"github.com/tailscale/win"
	"github.com/yungor-ai/yungor-keydock-connect/internal/desktop"
	"golang.org/x/sys/windows"
)

const exportDiagnosticsLabel = "导出诊断日志"

type nativeWindow struct {
	app                           *walk.Application
	controller                    *desktop.Controller
	window                        *walk.MainWindow
	scroll                        *walk.ScrollView
	server, key                   *walk.LineEdit
	device                        *walk.Label
	message                       *walk.TextLabel
	connectionNotice              *walk.TextLabel
	proxyHint, tunHint            *walk.TextLabel
	subscriptionHint              *walk.TextLabel
	proxyNotice                   *walk.TextLabel
	diagnosticsNotice             *walk.TextLabel
	clockHint                     *walk.TextLabel
	troubleshootingNotice         *walk.TextLabel
	troubleshooting               *walk.Composite
	proxyState, proxyUpdatedAt    *walk.Label
	proxyModes                    *walk.ComboBox
	rows                          []*checkRow
	colors                        windowColors
	connect, proxy, restore       *walk.PushButton
	export, recheck, timeSettings *walk.PushButton
	downloads, checkUpdates       *walk.PushButton
	state                         desktop.State
	cleaned                       bool
	results                       map[string]desktop.Check
	cancel                        context.CancelFunc
	quitting                      bool
	choosingExportPath            bool
	confirmingAction              bool
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
	ui.proxyModes.CurrentIndexChanged().Attach(func() {
		ui.setNotice(ui.proxyNotice, "", ui.colors.muted)
		ui.renderProxy()
	})
	// The Walk layout worker closes its quit channel when App.Exit cancels
	// the app context. A form may only be disposed while that worker is alive.
	enteredLoop := false
	defer func() {
		if !enteredLoop && !ui.window.IsDisposed() {
			ui.window.Dispose()
		}
	}()
	ui.window.SetExitOnClose(false)
	var tray *walk.NotifyIcon
	if windowsTaskbarAvailable() {
		if executable, err := os.Executable(); err == nil {
			guid := trayGUIDForPath(executable)
			tray = ui.createTray(icon, func() (*walk.NotifyIcon, error) { return walk.NewNotifyIconWithGUID(guid) })
		}
		if tray == nil {
			tray = ui.createTray(icon, walk.NewNotifyIcon)
		}
	}
	if tray != nil {
		defer tray.Dispose()
		ui.setMessage("关闭窗口后可从系统托盘重新打开。", ui.colors.muted)
	} else {
		ui.setMessage("关闭窗口将退出接入助手。", ui.colors.muted)
	}
	cleanup, err := watchActivation(dir, func() { app.Synchronize(ui.show) })
	if err != nil {
		return err
	}
	defer cleanup()
	ui.window.Closing().Attach(func(canceled *bool, _ walk.CloseReason) {
		*canceled = true
		if tray == nil || !windowsTaskbarAvailable() {
			ui.quit()
			return
		}
		ui.key.SetText("")
		ui.window.Hide()
	})
	ui.refresh()
	ui.server.SetText(ui.state.ServerURL)
	ui.updateConnectionGuidance()
	ui.server.TextChanged().Attach(ui.updateConnectionGuidance)
	ui.key.TextChanged().Attach(ui.updateConnectionGuidance)
	ui.renderChecks()
	ui.window.Show() // Starts the form exactly once, after all handlers are ready.
	ui.fitWindowToMonitor()
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
				if ui.cancel == nil && ui.state.ProxyMode != controller.ActiveProxyMode() {
					ui.refresh()
					if ui.state.ProxyMode == "" {
						ui.setNotice(ui.proxyNotice, "代理已停止。可重新启用或导出诊断日志。", ui.colors.danger)
					}
				}
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

func trayGUIDForPath(executable string) windows.GUID {
	// Explorer binds a tray GUID to the executable path, so installations in
	// different locations must use distinct identities across launches.
	path := strings.ToLower(filepath.Clean(executable))
	digest := sha256.Sum256([]byte("ai.yungor.keydock-connect.tray\x00" + path))
	digest[7] = digest[7]&0x0f | 0x80 // UUID version 8, application-defined.
	digest[8] = digest[8]&0x3f | 0x80 // RFC UUID variant.
	return windows.GUID{
		Data1: binary.LittleEndian.Uint32(digest[:4]),
		Data2: binary.LittleEndian.Uint16(digest[4:6]),
		Data3: binary.LittleEndian.Uint16(digest[6:8]),
		Data4: [8]byte(digest[8:16]),
	}
}

func windowsTaskbarAvailable() bool {
	// Walk can return a deferred notification icon before Explorer is ready.
	bar := win.APPBARDATA{CbSize: uint32(unsafe.Sizeof(win.APPBARDATA{}))}
	return win.SHAppBarMessage(win.ABM_GETTASKBARPOS, &bar) != 0
}

func (ui *nativeWindow) createTray(icon *walk.Icon, register func() (*walk.NotifyIcon, error)) *walk.NotifyIcon {
	tray, err := register()
	if err != nil {
		return nil
	}
	ready := false
	defer func() {
		if !ready {
			tray.Dispose()
		}
	}()
	if err := tray.SetIcon(icon); err != nil {
		return nil
	}
	if err := tray.SetToolTip("KeyDock Connect"); err != nil {
		return nil
	}
	addAction := func(label string, fn func()) error {
		action := walk.NewAction()
		if err := action.SetText(label); err != nil {
			return err
		}
		action.Triggered().Attach(fn)
		return tray.ContextMenu().Actions().Add(action)
	}
	if err := addAction("打开 KeyDock Connect", ui.show); err != nil {
		return nil
	}
	if err := addAction("退出接入助手", ui.quit); err != nil {
		return nil
	}
	tray.MouseUp().Attach(func(_, _ int, b walk.MouseButton) {
		if b == walk.LeftButton {
			ui.show()
		}
	})
	if err := tray.SetVisible(true); err != nil {
		return nil
	}
	ready = true
	return tray
}

func (ui *nativeWindow) fitWindowToMonitor() {
	// Walk initially measures the scrollable form at its minimum size.
	// Size the shown form explicitly so those bounds never become the default.
	preferred := ui.window.SizeFrom96DPI(walk.Size{Width: 720, Height: 710})
	workArea := ui.window.Monitor().WorkArea()
	bounds := ui.window.BoundsPixels()
	bounds.Width, bounds.Height = preferred.Width, preferred.Height
	if workArea.Width > 0 && workArea.Height > 0 {
		bounds.Width = min(bounds.Width, workArea.Width)
		bounds.Height = min(bounds.Height, workArea.Height)
		bounds.X = workArea.X + (workArea.Width-bounds.Width)/2
		bounds.Y = workArea.Y + (workArea.Height-bounds.Height)/2
	}
	ui.window.SetBoundsPixels(bounds)
}

func (ui *nativeWindow) show() {
	if ui.quitting || ui.choosingExportPath || ui.confirmingAction {
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
	device := "尚未接入设备"
	switch {
	case ui.state.Device != "" && ui.state.Model != "":
		device = ui.state.Device + " · " + ui.state.Model
	case ui.state.Device != "":
		device = ui.state.Device
	case ui.state.Paired:
		device = "已接入设备"
	}
	ui.device.SetText(device)
	ui.enable()
}

func (ui *nativeWindow) updateConnectionGuidance() {
	sameServer := ui.state.Paired && strings.TrimRight(strings.TrimSpace(ui.server.Text()), "/") == ui.state.ServerURL
	if sameServer {
		ui.key.SetCueBanner("可留空，使用已保存密钥")
		ui.setNotice(ui.connectionNotice, "已保存设备密钥，可留空后直接连接并测试。", ui.colors.muted)
		return
	}
	ui.key.SetCueBanner("管理员签发的 Device Key")
	ui.setNotice(ui.connectionNotice, "请填写管理员提供的服务器地址和设备密钥。", ui.colors.muted)
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
	ui.renderNetworkAction()
}

func (ui *nativeWindow) openTimeSettings() {
	if windows.GetCurrentProcessToken().IsElevated() {
		ui.setNotice(ui.troubleshootingNotice, "请从 Windows 开始菜单打开设置，进入「时间和语言」→「日期和时间」。", ui.colors.accent)
		return
	}
	settings := windows.StringToUTF16Ptr("ms-settings:dateandtime")
	if err := windows.ShellExecute(windows.Handle(ui.window.Handle()), nil, settings, nil, nil, win.SW_SHOWNORMAL); err != nil {
		ui.setNotice(ui.troubleshootingNotice, "无法打开时间设置。请从 Windows 设置中进入「时间和语言」→「日期和时间」。", ui.colors.danger)
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
			ui.setNotice(ui.diagnosticsNotice, "无法打开诊断日志保存窗口", ui.colors.danger)
		}
		return
	}
	path := windows.UTF16ToString(filename[:])
	ui.runAction(ui.diagnosticsNotice, "正在保存诊断日志…", "诊断日志已保存，可发送给管理员。", func(context.Context) error {
		if err := ui.controller.ExportDiagnostics(path); err != nil {
			return errors.New("无法保存诊断日志，请选择可写入的位置后重试")
		}
		return nil
	})
}

func (ui *nativeWindow) startConnect() {
	if ui.cancel != nil || ui.quitting {
		return
	}
	server, key := strings.TrimSpace(ui.server.Text()), ui.key.Text()
	ui.key.SetText("")
	for _, stage := range connectionStages {
		delete(ui.results, stage.id)
	}
	ui.renderChecks()
	ui.runTask(ui.connectionNotice, "正在连接并测试…", func(ctx context.Context) error {
		return ui.controller.Connect(ctx, server, key, func(result desktop.Check) {
			ui.app.Synchronize(func() { ui.results[result.Stage] = result; ui.renderChecks() })
		})
	}, func(err error) {
		switch {
		case err == nil:
			ui.updateConnectionGuidance()
			ui.setNotice(ui.connectionNotice, "设备接入完成。可查看下方的连接检查。", ui.colors.success)
		case ui.results["device"].Status == "failed":
			message := ui.results["device"].Message
			if strings.Contains(message, "服务拒绝了当前密钥") {
				message += "。请核对服务器地址，并重新填写该设备的密钥。"
			}
			ui.setNotice(ui.connectionNotice, message, ui.colors.danger)
		case ui.results["device"].Status == "ok":
			ui.updateConnectionGuidance()
			ui.setNotice(ui.connectionNotice, "设备已接入，部分检测未通过。请查看下方的连接检查。", ui.colors.danger)
		default:
			ui.setNotice(ui.connectionNotice, err.Error(), ui.colors.danger)
		}
	})
}

func (ui *nativeWindow) restoreCodex() {
	const title = "恢复原 Codex 配置"
	const message = "这会撤销接入助手为 Codex 写入的 KeyDock 配置。需要再次使用时，请点击「连接并测试」。"
	ui.confirmingAction = true
	choice := walk.MsgBox(ui.window, title, message, walk.MsgBoxOKCancel|walk.MsgBoxIconWarning|walk.MsgBoxDefButton2)
	ui.confirmingAction = false
	if choice != win.IDOK {
		return
	}
	ui.runTask(ui.message, "正在恢复 Codex 配置…", func(context.Context) error {
		return ui.controller.Restore()
	}, func(err error) {
		if err != nil {
			ui.setMessage(err.Error(), ui.colors.danger)
			return
		}
		delete(ui.results, "codex")
		delete(ui.results, "model")
		ui.renderChecks()
		ui.setNotice(ui.connectionNotice, "已恢复原 Codex 配置，再次使用时请连接并测试。", ui.colors.muted)
		ui.setMessage("已恢复原 Codex 配置", ui.colors.success)
	})
}

func (ui *nativeWindow) runAction(notice *walk.TextLabel, progress, done string, action func(context.Context) error) {
	ui.runTask(notice, progress, action, func(err error) {
		if err != nil {
			ui.setNotice(notice, err.Error(), ui.colors.danger)
		} else {
			ui.setNotice(notice, done, ui.colors.success)
		}
	})
}

func (ui *nativeWindow) runTask(notice *walk.TextLabel, progress string, action func(context.Context) error, completed func(error)) {
	if ui.cancel != nil || ui.quitting || ui.choosingExportPath || ui.confirmingAction {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ui.cancel = cancel
	ui.enable()
	ui.setNotice(notice, progress, ui.colors.accent)
	go func() {
		err := action(ctx)
		cancel()
		ui.app.Synchronize(func() {
			ui.cancel = nil
			if ui.quitting {
				if notice != ui.message {
					ui.setNotice(notice, "", ui.colors.muted)
				}
				ui.finishQuit()
				return
			}
			ui.refresh()
			completed(err)
		})
	}()
}

func (ui *nativeWindow) setMessage(text string, color walk.Color) {
	ui.setNotice(ui.message, text, color)
}

func (ui *nativeWindow) setNotice(label *walk.TextLabel, text string, color walk.Color) {
	label.SetTextColor(color)
	label.SetText(strings.ReplaceAll(text, "\n", "　"))
	label.SetToolTipText(text)
	ui.setControlVisible(label, text != "")
}

func (ui *nativeWindow) setControlVisible(control walk.Widget, visible bool) {
	handle := control.Handle()
	// Test the control's own visibility even while the window is in the tray.
	if (win.GetWindowLong(handle, win.GWL_STYLE)&win.WS_VISIBLE != 0) == visible {
		return
	}
	command := int32(win.SW_HIDE)
	if visible {
		command = win.SW_SHOWNA
	}
	win.ShowWindow(handle, command)
	ui.window.RequestLayout()
}

func (ui *nativeWindow) quit() {
	if ui.quitting || ui.choosingExportPath || ui.confirmingAction {
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
				// The network watcher may have published its completion while quitting.
				if ui.networkCheckRunning() {
					ui.controller.RecheckNetwork()
				}
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
