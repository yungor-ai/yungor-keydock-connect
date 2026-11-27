//go:build windows

package main

import (
	"github.com/tailscale/walk"
	d "github.com/tailscale/walk/declarative"
	"github.com/tailscale/win"
	keydockconnect "github.com/yungor-ai/yungor-keydock-connect"
)

type windowColors struct {
	ink, muted, accent, success, danger       walk.Color
	page, header, card, proxy, badge, warning d.Brush
}

func nativeColors() windowColors {
	if walk.IsHighContrastEnabled() {
		// Walk resolves a zero text color and this brush from the Windows theme.
		surface := d.SystemColorBrush{Color: walk.SysColorWindow}
		return windowColors{page: surface, header: surface, card: surface, proxy: surface, badge: surface, warning: surface}
	}
	return windowColors{
		ink: walk.RGB(25, 49, 55), muted: walk.RGB(75, 98, 108), accent: walk.RGB(0, 102, 111),
		success: walk.RGB(15, 102, 77), danger: walk.RGB(168, 48, 52),
		page: d.SolidColorBrush{Color: walk.RGB(240, 244, 245)}, header: d.SolidColorBrush{Color: walk.RGB(222, 241, 239)},
		card: d.WhiteBrush{}, proxy: d.SolidColorBrush{Color: walk.RGB(229, 241, 244)}, badge: d.SolidColorBrush{Color: walk.RGB(240, 245, 246)},
		warning: d.SolidColorBrush{Color: walk.RGB(255, 246, 224)},
	}
}

type checkStage struct {
	id, title, description string
}

var connectionStages = []checkStage{
	{"device", "设备授权", "从服务器获取设备配置"},
	{"codex", "Codex 配置", "写入本机 Codex 配置"},
	{"model", "模型连通", "发送模型测试请求"},
	{"proxy", "代理连通", "通过 SS 节点测试 HTTPS 请求"},
}

var networkStages = []checkStage{
	{"google", "Google 连通", "用 TCP HTTPS 检测 www.google.com"},
	{"egress", "出口 IPv4", "TCP HTTPS 出口与 KeyDock IPv4 比较"},
}

type checkRow struct {
	stage  checkStage
	status *walk.Label
	detail *walk.TextLabel
}

func uiFont(points int, bold bool) d.Font {
	return d.Font{Family: "Microsoft YaHei UI", PointSize: points, SemiBold: bold}
}

func (ui *nativeWindow) build(icon *walk.Icon) error {
	colors := ui.colors
	heading := func(text string) d.Label {
		return d.Label{Text: text, Font: uiFont(13, true), TextColor: colors.ink, MinSize: d.Size{Height: 28}, MaxSize: d.Size{Height: 28}}
	}
	subheading := func(text string) d.Label {
		return d.Label{Text: text, Font: uiFont(10, true), TextColor: colors.ink}
	}
	note := func(target **walk.TextLabel) d.TextLabel {
		return d.TextLabel{AssignTo: target, NoPrefix: true, MinSize: d.Size{Width: 130}, TextColor: colors.muted}
	}
	section := d.VBox{Margins: d.Margins{Left: 18, Top: 10, Right: 18, Bottom: 12}, Spacing: 6}
	inline := d.HBox{MarginsZero: true, Spacing: 8, Alignment: d.AlignHNearVCenter}

	header := d.Composite{
		Background: colors.header,
		MinSize:    d.Size{Height: 64},
		MaxSize:    d.Size{Height: 64},
		Layout:     d.HBox{Margins: d.Margins{Left: 22, Top: 9, Right: 22, Bottom: 10}, Spacing: 12},
		Children: []d.Widget{
			d.ImageView{Image: icon, Mode: d.ImageViewModeZoom, MinSize: d.Size{Width: 40, Height: 40}, MaxSize: d.Size{Width: 40, Height: 40}},
			d.Composite{Layout: d.VBox{MarginsZero: true, Spacing: 2}, Children: []d.Widget{
				d.Label{Text: "KeyDock Connect", Font: uiFont(16, true), TextColor: colors.ink},
				d.Label{AssignTo: &ui.device, TextColor: colors.muted, NoPrefix: true, EllipsisMode: d.EllipsisEnd},
			}},
		},
	}
	fieldLayout := d.VBox{MarginsZero: true, Spacing: 4}
	connection := d.Composite{
		Background: colors.card, Layout: section,
		Children: []d.Widget{
			heading("设备接入"),
			d.Composite{Layout: d.HBox{MarginsZero: true, Spacing: 10, Alignment: d.AlignHNearVFar}, Children: []d.Widget{
				d.Composite{StretchFactor: 3, Layout: fieldLayout, Children: []d.Widget{
					d.Label{Text: "服务器地址", TextColor: colors.muted},
					d.LineEdit{AssignTo: &ui.server, Accessibility: d.Accessibility{Name: "服务器地址"},
						CueBanner: "https://keydock.example.com", MinSize: d.Size{Width: 125, Height: 32}},
				}},
				d.Composite{StretchFactor: 2, Layout: fieldLayout, Children: []d.Widget{
					d.Label{Text: "设备密钥", TextColor: colors.muted},
					d.LineEdit{AssignTo: &ui.key, Accessibility: d.Accessibility{Name: "设备密钥 (Device Key)"},
						CueBanner: "管理员签发的 Device Key", PasswordMode: true, MinSize: d.Size{Width: 116, Height: 32}},
				}},
				d.PushButton{AssignTo: &ui.connect, Text: "连接并测试", Font: uiFont(10, true), Default: true,
					Alignment: d.AlignHNearVFar, MinSize: d.Size{Width: 110, Height: 32}, OnClicked: ui.startConnect},
			}},
			note(&ui.connectionNotice),
		},
	}

	proxy := d.Composite{
		Background: colors.proxy, Layout: section,
		Children: []d.Widget{
			d.Composite{Layout: inline, Children: []d.Widget{
				heading("网络代理"),
				d.HSpacer{},
				d.Label{AssignTo: &ui.proxyState, Font: uiFont(10, true), TextColor: colors.muted},
			}},
			d.Composite{Layout: inline, Children: []d.Widget{
				d.Label{Text: "代理模式", TextColor: colors.muted},
				d.ComboBox{AssignTo: &ui.proxyModes, CurrentIndex: 0, Model: []string{"系统代理", "TUN（虚拟网卡）"},
					Accessibility: d.Accessibility{Name: "代理模式"}, MinSize: d.Size{Width: 130}, MaxSize: d.Size{Width: 130}},
				d.HSpacer{},
				d.PushButton{AssignTo: &ui.proxy, Text: "启用系统代理", MinSize: d.Size{Width: 116, Height: 32},
					MaxSize: d.Size{Width: 116}, OnClicked: ui.toggleProxy},
			}},
			note(&ui.proxyHint),
			note(&ui.tunHint),
			d.Label{AssignTo: &ui.proxyUpdatedAt, Font: uiFont(10, false), TextColor: colors.muted,
				MinSize: d.Size{Width: 250}},
			note(&ui.subscriptionHint),
			note(&ui.proxyNotice),
		},
	}

	troubleshooting := d.Composite{
		AssignTo: &ui.troubleshooting, Background: colors.warning,
		Layout: d.VBox{Margins: d.Margins{Left: 10, Top: 5, Right: 10, Bottom: 8}, Spacing: 4},
		Children: []d.Widget{
			d.Composite{Layout: inline, Children: []d.Widget{
				subheading("代理故障排查"),
				d.HSpacer{},
				d.PushButton{AssignTo: &ui.timeSettings, Text: "打开时间设置", UseLayoutFlags: true,
					MinSize: d.Size{Width: 116, Height: 28}, OnClicked: ui.openTimeSettings},
			}},
			d.TextLabel{AssignTo: &ui.clockHint, NoPrefix: true, MinSize: d.Size{Width: 160}, TextColor: colors.ink},
			d.TextLabel{Text: "若 Clash 等其他代理工具开启了 TUN（虚拟网卡），可在对应工具中关闭 TUN，再点「连接并测试」。",
				NoPrefix: true, MinSize: d.Size{Width: 160}, TextColor: colors.ink},
			note(&ui.troubleshootingNotice),
		},
	}
	checks := d.Composite{Background: colors.card, Layout: section, Children: []d.Widget{
		d.Composite{Layout: inline, Children: []d.Widget{
			heading("连接检查"),
			d.HSpacer{},
			d.PushButton{AssignTo: &ui.export, Text: exportDiagnosticsLabel, UseLayoutFlags: true,
				MinSize: d.Size{Width: 124, Height: 28}, OnClicked: ui.exportDiagnostics},
		}},
		note(&ui.diagnosticsNotice),
		subheading("设备接入检测"),
		ui.checkGrid(connectionStages),
		troubleshooting,
		d.Composite{Layout: inline, Children: []d.Widget{
			subheading("网络访问检测"),
			d.HSpacer{},
			d.PushButton{AssignTo: &ui.recheck, Text: "重新检测网络", UseLayoutFlags: true,
				MinSize: d.Size{Width: 116, Height: 28}, OnClicked: ui.controller.RecheckNetwork},
		}},
		ui.checkGrid(networkStages),
	}}
	body := d.ScrollView{AssignTo: &ui.scroll, Background: colors.page,
		Layout:   d.VBox{Margins: d.Margins{Left: 16, Top: 10, Right: 16, Bottom: 10}, Spacing: 10, Alignment: d.AlignHNearVNear},
		Children: []d.Widget{connection, proxy, checks, d.VSpacer{}},
	}
	footer := d.Composite{
		Background: colors.card, Layout: d.VBox{Margins: d.Margins{Left: 16, Top: 8, Right: 16, Bottom: 9}, Spacing: 4},
		Children: []d.Widget{
			d.Composite{Layout: inline, Children: []d.Widget{
				d.PushButton{AssignTo: &ui.downloads, Text: "软件下载", UseLayoutFlags: true,
					ToolTipText: "在浏览器打开当前服务器的软件下载页", MinSize: d.Size{Height: 28}, OnClicked: ui.openSoftware},
				d.PushButton{AssignTo: &ui.checkUpdates, Text: "检查更新", UseLayoutFlags: true,
					MinSize: d.Size{Height: 28}, OnClicked: ui.checkForUpdate},
				d.HSpacer{},
				d.PushButton{AssignTo: &ui.restore, Text: "恢复原 Codex 配置", UseLayoutFlags: true,
					MinSize: d.Size{Height: 28}, OnClicked: ui.restoreCodex},
				d.PushButton{Text: "退出接入助手", UseLayoutFlags: true, MinSize: d.Size{Height: 28}, OnClicked: ui.quit},
			}},
			note(&ui.message),
		},
	}
	err := (d.MainWindow{
		AssignTo: &ui.window, Title: "KeyDock Connect v" + keydockconnect.Version(), Visible: false, Icon: icon,
		Font: uiFont(10, false), MinSize: d.Size{Width: 520, Height: 360},
		Layout: d.VBox{MarginsZero: true, SpacingZero: true}, Children: []d.Widget{header, body, footer},
	}).Create()
	if err != nil {
		return err
	}
	// An empty native toolbar otherwise covers the header in some print captures.
	toolbar := ui.window.ToolBar()
	ui.window.SetToolBar(nil)
	toolbar.Dispose()
	ui.revealFocusedControls()
	return nil
}

func (ui *nativeWindow) checkGrid(group []checkStage) d.Composite {
	grid := d.Composite{Layout: d.Grid{Columns: 3, MarginsZero: true, Spacing: 5, Alignment: d.AlignHNearVCenter}}
	for _, stage := range group {
		row := &checkRow{stage: stage}
		ui.rows = append(ui.rows, row)
		grid.Children = append(grid.Children,
			d.Label{Text: stage.title, TextColor: ui.colors.ink, MinSize: d.Size{Width: 88}, MaxSize: d.Size{Width: 88}},
			d.Label{AssignTo: &row.status, Background: ui.colors.badge, TextColor: ui.colors.muted, TextAlignment: d.AlignCenter,
				MinSize: d.Size{Width: 65, Height: 24}, MaxSize: d.Size{Width: 65, Height: 24}},
			d.TextLabel{AssignTo: &row.detail, TextColor: ui.colors.muted, NoPrefix: true, MinSize: d.Size{Width: 120},
				TextAlignment: d.AlignHNearVCenter},
		)
	}
	return grid
}

func (ui *nativeWindow) renderChecks() {
	for _, row := range ui.rows {
		stage := row.stage
		status, detail, color := "待检查", stage.description, ui.colors.muted
		detailColor := ui.colors.muted
		if result, ok := ui.results[stage.id]; ok {
			detail = result.Message
			switch result.Status {
			case "running":
				status, color = "进行中", ui.colors.accent
			case "ok":
				status, color = "完成", ui.colors.success
			case "failed":
				status, color = "失败", ui.colors.danger
			case "warning":
				status, color = "不一致", ui.colors.danger
				detailColor = ui.colors.danger
			case "skipped":
				status = "跳过"
			}
			switch {
			case stage.id == "google" && result.Status == "ok":
				status = "可访问"
			case stage.id == "google" && result.Status == "failed":
				status = "无法访问"
			case stage.id == "egress" && result.Status == "ok":
				status = "一致"
			case stage.id == "egress" && result.Status == "skipped":
				status = "未校验"
			}
		}
		row.status.SetTextColor(color)
		row.status.SetText(status)
		row.detail.SetTextColor(detailColor)
		row.detail.SetText(detail)
		row.detail.SetToolTipText(detail)
	}
	result := ui.results["proxy"]
	showHint := result.Status == "failed" && result.Hint != ""
	if showHint {
		ui.clockHint.SetText(result.Hint)
	} else {
		ui.setNotice(ui.troubleshootingNotice, "", ui.colors.muted)
	}
	ui.setControlVisible(ui.troubleshooting, showHint)
	ui.renderNetworkAction()
}

func (ui *nativeWindow) networkCheckRunning() bool {
	return ui.results["google"].Status == "running" || ui.results["egress"].Status == "running"
}

func (ui *nativeWindow) renderNetworkAction() {
	ui.recheck.SetEnabled(!ui.quitting && !ui.networkCheckRunning())
}

func (ui *nativeWindow) revealFocusedControls() {
	// ScrollView does not scroll focused children into view during keyboard navigation.
	for _, control := range []walk.Widget{ui.server, ui.key, ui.connect, ui.proxyModes, ui.proxy, ui.export, ui.timeSettings, ui.recheck} {
		control.FocusedChanged().Attach(func() {
			if !control.Focused() {
				return
			}
			var view, child win.RECT
			if !win.GetWindowRect(ui.scroll.Handle(), &view) || !win.GetWindowRect(control.Handle(), &child) ||
				child.Bottom-child.Top >= view.Bottom-view.Top {
				return
			}
			for child.Top < view.Top || child.Bottom > view.Bottom {
				direction := win.SB_LINEDOWN
				if child.Top < view.Top {
					direction = win.SB_LINEUP
				}
				win.SendMessage(ui.scroll.Handle(), win.WM_VSCROLL, uintptr(direction), 0)
				var next win.RECT
				if !win.GetWindowRect(control.Handle(), &next) || next == child {
					break
				}
				child = next
			}
		})
	}
}
