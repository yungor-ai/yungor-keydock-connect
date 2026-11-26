//go:build windows

package main

import (
	"context"
	"slices"

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

var stages = slices.Concat(connectionStages, []checkStage{
	{"google", "Google 连通", "检测 google.com 的访问状态"},
	{"egress", "出口 IPv4", "检测公网 IPv4 并与 KeyDock 服务器比较"},
})

type checkRow struct {
	status *walk.Label
	detail *walk.TextLabel
}

func uiFont(points int, bold bool) d.Font {
	return d.Font{Family: "Microsoft YaHei UI", PointSize: points, SemiBold: bold}
}

func (ui *nativeWindow) build(icon *walk.Icon) error {
	colors := ui.colors
	heading := func(text string) d.Label {
		return d.Label{Text: text, Font: uiFont(12, true), TextColor: colors.ink}
	}
	section := d.VBox{Margins: d.Margins{Left: 18, Top: 12, Right: 18, Bottom: 14}, Spacing: 8}

	header := d.Composite{
		Background: colors.header,
		Layout:     d.HBox{Margins: d.Margins{Left: 22, Top: 10, Right: 22, Bottom: 12}, Spacing: 14},
		Children: []d.Widget{
			d.ImageView{Image: icon, Mode: d.ImageViewModeZoom, MinSize: d.Size{Width: 44, Height: 44}, MaxSize: d.Size{Width: 44, Height: 44}},
			d.Composite{StretchFactor: 2, Layout: d.VBox{MarginsZero: true, SpacingZero: true}, Children: []d.Widget{
				d.Label{Text: "设备接入", Font: uiFont(19, true), TextColor: colors.ink,
					MinSize: d.Size{Height: 38}, MaxSize: d.Size{Height: 38}},
				d.TextLabel{AssignTo: &ui.device, NoPrefix: true, MinSize: d.Size{Width: 150}, TextColor: colors.muted},
			}},
		},
	}
	fieldLayout := d.VBox{MarginsZero: true, Spacing: 4}
	connection := d.Composite{
		Background: colors.card, Layout: section,
		Children: []d.Widget{
			d.Composite{Layout: d.HBox{MarginsZero: true, Spacing: 8, Alignment: d.AlignHNearVCenter}, Children: []d.Widget{
				heading("连接设置"),
				d.HSpacer{},
				d.PushButton{AssignTo: &ui.downloads, Text: "软件下载", UseLayoutFlags: true,
					ToolTipText: "在浏览器打开当前服务器的软件下载页", MinSize: d.Size{Height: 28}, OnClicked: ui.openSoftware},
				d.PushButton{AssignTo: &ui.checkUpdates, Text: "检查更新", UseLayoutFlags: true,
					MinSize: d.Size{Height: 28}, OnClicked: ui.checkForUpdate},
			}},
			d.Composite{Layout: d.HBox{MarginsZero: true, Spacing: 10, Alignment: d.AlignHNearVFar}, Children: []d.Widget{
				d.Composite{Layout: fieldLayout, Children: []d.Widget{
					d.Label{Text: "服务器地址", TextColor: colors.muted},
					d.LineEdit{AssignTo: &ui.server, Accessibility: d.Accessibility{Name: "服务器地址"}, CueBanner: "https://keydock.example.com", MinSize: d.Size{Height: 36}},
				}},
				d.Composite{Layout: fieldLayout, Children: []d.Widget{
					d.Label{Text: "Device Key", TextColor: colors.muted},
					d.LineEdit{AssignTo: &ui.key, Accessibility: d.Accessibility{Name: "Device Key"}, CueBanner: "管理员签发的设备密钥", PasswordMode: true, MinSize: d.Size{Height: 36}},
				}},
				d.PushButton{AssignTo: &ui.connect, Text: "连接并测试", Font: uiFont(10, true), Default: true,
					Alignment: d.AlignHNearVFar, MinSize: d.Size{Width: 110, Height: 32}, OnClicked: ui.startConnect},
			}},
		},
	}

	grid := d.Composite{Layout: d.Grid{Columns: 3, MarginsZero: true, Spacing: 8, Alignment: d.AlignHNearVCenter}}
	ui.rows = make([]checkRow, len(stages))
	for i, stage := range stages {
		row := &ui.rows[i]
		grid.Children = append(grid.Children,
			d.Label{Text: stage.title, TextColor: colors.ink, MinSize: d.Size{Width: 88}, MaxSize: d.Size{Width: 88}},
			d.Label{AssignTo: &row.status, Background: colors.badge, TextColor: colors.muted, TextAlignment: d.AlignCenter,
				MinSize: d.Size{Width: 60, Height: 26}, MaxSize: d.Size{Width: 60, Height: 26}},
			d.TextLabel{AssignTo: &row.detail, TextColor: colors.muted, NoPrefix: true, MinSize: d.Size{Width: 120},
				TextAlignment: d.AlignHNearVCenter},
		)
	}
	checks := d.Composite{Background: colors.card, Layout: section, Children: []d.Widget{
		d.Composite{Layout: d.HBox{MarginsZero: true, Spacing: 8, Alignment: d.AlignHNearVCenter}, Children: []d.Widget{
			heading("连接检查"),
			d.HSpacer{},
			d.PushButton{Text: "重新检测网络", UseLayoutFlags: true,
				MinSize: d.Size{Width: 116, Height: 28}, OnClicked: ui.controller.RecheckNetwork},
			d.PushButton{AssignTo: &ui.export, Text: exportDiagnosticsLabel, UseLayoutFlags: true,
				MinSize: d.Size{Width: 126, Height: 28}, OnClicked: ui.exportDiagnostics},
		}},
		grid,
	}}
	troubleshooting := d.Composite{
		AssignTo: &ui.troubleshooting, Background: colors.warning,
		Layout: d.VBox{Margins: d.Margins{Left: 18, Top: 8, Right: 18, Bottom: 10}, Spacing: 4},
		Children: []d.Widget{
			d.Composite{Layout: d.HBox{MarginsZero: true, Spacing: 8, Alignment: d.AlignHNearVCenter}, Children: []d.Widget{
				heading("代理故障排查"),
				d.HSpacer{},
				d.PushButton{AssignTo: &ui.timeSettings, Text: "打开时间设置", UseLayoutFlags: true,
					MinSize: d.Size{Width: 116, Height: 28}, OnClicked: ui.openTimeSettings},
			}},
			d.TextLabel{AssignTo: &ui.clockHint, NoPrefix: true, MinSize: d.Size{Width: 160}, TextColor: colors.ink},
			d.TextLabel{Text: "若 Clash 等其他代理工具开启了 TUN（虚拟网卡），可在对应工具中关闭 TUN，再点「连接并测试」。",
				NoPrefix: true, MinSize: d.Size{Width: 160}, TextColor: colors.ink},
		},
	}
	proxy := d.Composite{
		Background: colors.proxy, Layout: d.VBox{Margins: d.Margins{Left: 18, Top: 10, Right: 18, Bottom: 11}, Spacing: 4},
		Children: []d.Widget{
			d.Composite{Layout: d.HBox{MarginsZero: true, Spacing: 10, Alignment: d.AlignHNearVCenter}, Children: []d.Widget{
				d.Label{AssignTo: &ui.proxyState, Font: uiFont(12, true), TextColor: colors.ink},
				d.HSpacer{},
				d.ComboBox{AssignTo: &ui.proxyModes, CurrentIndex: 0, Model: []string{"系统代理", "TUN（虚拟网卡）"},
					Accessibility: d.Accessibility{Name: "代理模式"}, MinSize: d.Size{Width: 145}, MaxSize: d.Size{Width: 145}},
				d.PushButton{AssignTo: &ui.proxy, Text: "启用系统代理", MinSize: d.Size{Width: 110, Height: 32},
					MaxSize: d.Size{Width: 110}, OnClicked: ui.toggleProxy},
			}},
			d.Label{AssignTo: &ui.proxyUpdatedAt, Font: uiFont(10, true), TextColor: colors.ink},
			d.TextLabel{AssignTo: &ui.proxyHint, MinSize: d.Size{Width: 150}, TextColor: colors.muted},
		},
	}
	body := d.Composite{Background: colors.page,
		Layout:   d.VBox{Margins: d.Margins{Left: 16, Top: 12, Right: 16, Bottom: 12}, Spacing: 10, Alignment: d.AlignHNearVNear},
		Children: []d.Widget{connection, checks, troubleshooting, proxy, d.VSpacer{}},
	}
	footer := d.Composite{
		Background: colors.card, Layout: d.HBox{Margins: d.Margins{Left: 20, Top: 8, Right: 16, Bottom: 8}, Spacing: 8},
		Children: []d.Widget{
			d.TextLabel{AssignTo: &ui.message, NoPrefix: true, StretchFactor: 2, MinSize: d.Size{Width: 125}, Text: "关闭窗口可收至系统托盘。", TextAlignment: d.AlignHNearVCenter, TextColor: colors.muted},
			d.PushButton{AssignTo: &ui.restore, Text: "恢复原 Codex 配置", UseLayoutFlags: true, MinSize: d.Size{Height: 28}, OnClicked: func() {
				ui.runAction("正在恢复 Codex 配置…", "已恢复原 Codex 配置", func(context.Context) error { return ui.controller.Restore() })
			}},
			d.PushButton{Text: "退出接入助手", UseLayoutFlags: true, MinSize: d.Size{Height: 28}, OnClicked: ui.quit},
		},
	}
	err := (d.MainWindow{
		AssignTo: &ui.window, Title: "KeyDock Connect v" + keydockconnect.Version(), Visible: false, Icon: icon,
		Font: uiFont(10, false), Size: d.Size{Width: 680, Height: 638}, MinSize: d.Size{Width: 520, Height: 543},
		Layout: d.VBox{MarginsZero: true, SpacingZero: true}, Children: []d.Widget{header, body, footer},
	}).Create()
	if err != nil {
		return err
	}
	// An empty native toolbar otherwise covers the header in some print captures.
	toolbar := ui.window.ToolBar()
	ui.window.SetToolBar(nil)
	toolbar.Dispose()
	return nil
}

func (ui *nativeWindow) renderChecks() {
	for i, stage := range stages {
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
		row := &ui.rows[i]
		row.status.SetTextColor(color)
		row.status.SetText(status)
		row.detail.SetTextColor(detailColor)
		row.detail.SetText(detail)
		row.detail.SetToolTipText(detail)
	}
	result := ui.results["proxy"]
	visibility := int32(win.SW_HIDE)
	if result.Status == "failed" && result.Hint != "" {
		ui.clockHint.SetText(result.Hint)
		visibility = win.SW_SHOWNA
	}
	// Apply the panel's own visibility even when the main window is hidden in the tray.
	win.ShowWindow(ui.troubleshooting.Handle(), visibility)
}
