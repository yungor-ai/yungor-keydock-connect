//go:build windows

package main

import (
	"context"

	"github.com/tailscale/walk"
	d "github.com/tailscale/walk/declarative"
	keydockconnect "github.com/yungor-ai/yungor-keydock-connect"
)

type windowColors struct {
	ink, muted, accent, success, danger walk.Color
	page, header, card, proxy, badge    d.Brush
}

func nativeColors() windowColors {
	if walk.IsHighContrastEnabled() {
		// Walk resolves a zero text color and this brush from the Windows theme.
		surface := d.SystemColorBrush{Color: walk.SysColorWindow}
		return windowColors{page: surface, header: surface, card: surface, proxy: surface, badge: surface}
	}
	return windowColors{
		ink: walk.RGB(25, 49, 55), muted: walk.RGB(75, 98, 108), accent: walk.RGB(0, 102, 111),
		success: walk.RGB(15, 102, 77), danger: walk.RGB(168, 48, 52),
		page: d.SolidColorBrush{Color: walk.RGB(240, 244, 245)}, header: d.SolidColorBrush{Color: walk.RGB(222, 241, 239)},
		card: d.WhiteBrush{}, proxy: d.SolidColorBrush{Color: walk.RGB(229, 241, 244)}, badge: d.SolidColorBrush{Color: walk.RGB(240, 245, 246)},
	}
}

type checkStage struct {
	id, title, description string
}

var stages = []checkStage{
	{"device", "设备授权", "从服务器获取设备配置"},
	{"codex", "Codex 配置", "写入本机 Codex 配置"},
	{"model", "模型连通", "发送模型测试请求"},
	{"proxy", "代理连通", "通过 SS 节点测试 HTTPS 请求"},
}

type checkRow struct {
	status, detail *walk.Label
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
			d.Composite{Layout: d.VBox{MarginsZero: true, SpacingZero: true}, Children: []d.Widget{
				d.Label{Text: "设备接入", Font: uiFont(19, true), TextColor: colors.ink,
					MinSize: d.Size{Height: 38}, MaxSize: d.Size{Height: 38}},
				d.Label{AssignTo: &ui.device, NoPrefix: true, EllipsisMode: d.EllipsisEnd, TextColor: colors.muted},
			}},
			d.HSpacer{},
			d.PushButton{AssignTo: &ui.launch, Text: "打开 Codex", MinSize: d.Size{Width: 104, Height: 32}, OnClicked: func() {
				ui.runAction("正在打开 Codex…", "已打开 Codex", func(context.Context) error { return ui.controller.OpenCodex() })
			}},
		},
	}
	fieldLayout := d.VBox{MarginsZero: true, Spacing: 4}
	connection := d.Composite{
		Background: colors.card, Layout: section,
		Children: []d.Widget{
			heading("连接设置"),
			d.Composite{Layout: d.HBox{MarginsZero: true, Spacing: 10, Alignment: d.AlignHNearVFar}, Children: []d.Widget{
				d.Composite{Layout: fieldLayout, Children: []d.Widget{
					d.Label{Text: "服务器地址", TextColor: colors.muted},
					d.LineEdit{AssignTo: &ui.server, Accessibility: d.Accessibility{Name: "服务器地址"}, CueBanner: "https://keydock.example.com", MinSize: d.Size{Height: 30}},
				}},
				d.Composite{Layout: fieldLayout, Children: []d.Widget{
					d.Label{Text: "Device Key", TextColor: colors.muted},
					d.LineEdit{AssignTo: &ui.key, Accessibility: d.Accessibility{Name: "Device Key"}, CueBanner: "管理员签发的设备密钥", PasswordMode: true, MinSize: d.Size{Height: 30}},
				}},
				d.PushButton{AssignTo: &ui.connect, Text: "连接并测试", Font: uiFont(10, true), Default: true,
					Alignment: d.AlignHNearVFar, MinSize: d.Size{Width: 110, Height: 32}, OnClicked: ui.startConnect},
			}},
		},
	}

	checks := d.Composite{Background: colors.card, Layout: section, Children: []d.Widget{heading("连接检查")}}
	ui.rows = make([]checkRow, len(stages))
	for i, stage := range stages {
		row := &ui.rows[i]
		checks.Children = append(checks.Children, d.Composite{
			Layout: d.HBox{MarginsZero: true, Spacing: 12},
			Children: []d.Widget{
				d.Label{Text: stage.title, TextColor: colors.ink, MinSize: d.Size{Width: 88}, MaxSize: d.Size{Width: 88}},
				d.Label{AssignTo: &row.status, Background: colors.badge, TextColor: colors.muted, TextAlignment: d.AlignCenter,
					MinSize: d.Size{Width: 60, Height: 26}, MaxSize: d.Size{Width: 60, Height: 26}},
				d.Label{AssignTo: &row.detail, TextColor: colors.muted, EllipsisMode: d.EllipsisEnd, NoPrefix: true},
			},
		})
	}
	proxy := d.Composite{
		Background: colors.proxy, Layout: d.HBox{Margins: d.Margins{Left: 18, Top: 10, Right: 18, Bottom: 11}, Spacing: 12},
		Children: []d.Widget{
			d.Composite{Layout: d.VBox{MarginsZero: true, Spacing: 4}, Children: []d.Widget{
				d.Label{AssignTo: &ui.proxyState, Font: uiFont(12, true), TextColor: colors.ink},
				d.Label{AssignTo: &ui.proxyHint, EllipsisMode: d.EllipsisEnd, TextColor: colors.muted},
			}},
			d.HSpacer{},
			d.PushButton{AssignTo: &ui.proxy, Text: "启用系统代理", MinSize: d.Size{Width: 110, Height: 32}, OnClicked: func() {
				on := !ui.state.Proxy
				ui.runAction("正在更新系统代理…", "已更新 Windows 代理设置", func(context.Context) error { return ui.controller.SetProxy(on) })
			}},
		},
	}
	body := d.Composite{Background: colors.page,
		Layout:   d.VBox{Margins: d.Margins{Left: 16, Top: 12, Right: 16, Bottom: 12}, Spacing: 10, Alignment: d.AlignHNearVNear},
		Children: []d.Widget{connection, checks, proxy, d.VSpacer{}},
	}
	footer := d.Composite{
		Background: colors.card, Layout: d.HBox{Margins: d.Margins{Left: 20, Top: 8, Right: 16, Bottom: 8}, Spacing: 8},
		Children: []d.Widget{
			d.Label{AssignTo: &ui.message, NoPrefix: true, Text: "关闭窗口可收至系统托盘。", EllipsisMode: d.EllipsisEnd, TextColor: colors.muted},
			d.HSpacer{},
			d.PushButton{AssignTo: &ui.restore, Text: "恢复原 Codex 配置", MinSize: d.Size{Height: 28}, OnClicked: func() {
				ui.runAction("正在恢复 Codex 配置…", "已恢复原 Codex 配置", func(context.Context) error { return ui.controller.Restore() })
			}},
			d.PushButton{Text: "退出接入助手", MinSize: d.Size{Height: 28}, OnClicked: ui.quit},
		},
	}
	err := (d.MainWindow{
		AssignTo: &ui.window, Title: "KeyDock Connect v" + keydockconnect.Version(), Visible: false, Icon: icon,
		Font: uiFont(10, false), Size: d.Size{Width: 680, Height: 570}, MinSize: d.Size{Width: 520, Height: 475},
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
		if result, ok := ui.results[stage.id]; ok {
			detail = result.Message
			switch result.Status {
			case "running":
				status, color = "进行中", ui.colors.accent
			case "ok":
				status, color = "完成", ui.colors.success
			case "failed":
				status, color = "失败", ui.colors.danger
			case "skipped":
				status = "跳过"
			}
		}
		row := &ui.rows[i]
		row.status.SetTextColor(color)
		row.status.SetText(status)
		row.detail.SetText(detail)
		row.detail.SetToolTipText(detail)
	}
}
