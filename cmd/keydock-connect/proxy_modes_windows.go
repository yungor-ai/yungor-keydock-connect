//go:build windows

package main

import (
	"context"

	"github.com/tailscale/walk"
	"github.com/yungor-ai/yungor-keydock-connect/internal/proxy"
	"golang.org/x/sys/windows"
)

const tunModeIndex = 1

func (ui *nativeWindow) selectedProxyMode() proxy.Mode {
	if ui.proxyModes.CurrentIndex() == tunModeIndex {
		return proxy.ModeTUN
	}
	return proxy.ModeSystem
}

func proxyAction(selected, active proxy.Mode) string {
	action := "启用"
	switch {
	case selected == active:
		action = "关闭"
	case active != "":
		action = "切换至"
	}
	if selected == proxy.ModeTUN {
		return action + " TUN"
	}
	return action + "系统代理"
}

func (ui *nativeWindow) renderProxy() {
	selected, current := ui.selectedProxyMode(), ui.state.ProxyMode
	active := current != ""
	idle := ui.cancel == nil && !ui.quitting
	title := "系统代理未启用"
	switch {
	case current == proxy.ModeTUN:
		title = "TUN 已启用"
	case active:
		title = "系统代理已启用"
	case selected == proxy.ModeTUN:
		title = "TUN 未启用"
	}
	ui.proxyState.SetText(title)
	stateColor := ui.colors.muted
	if active {
		stateColor = ui.colors.success
	}
	ui.proxyState.SetTextColor(stateColor)

	var hint string
	switch {
	case !active && !ui.state.Subscription && ui.state.Paired:
		hint = "当前设备未分配 SS 订阅。"
	case !active && !ui.state.Subscription:
		hint = "请先接入设备并获取 SS 订阅。"
	case current == proxy.ModeTUN && selected == current:
		hint = "流量正通过虚拟网卡按订阅规则分流。"
	case active && selected == current:
		hint = "使用系统代理的应用流量正按订阅规则分流。"
	case selected == proxy.ModeTUN:
		hint = "通过虚拟网卡按订阅规则分流，覆盖绕过系统代理的应用。"
	default:
		hint = "启用后，使用系统代理的应用流量将按订阅规则分流。"
	}
	ui.setNotice(ui.proxyHint, hint, ui.colors.muted)

	tunHelp := ""
	if ui.state.Subscription && selected == proxy.ModeTUN && selected != current && !windows.GetCurrentProcessToken().IsElevated() {
		tunHelp = "启用 TUN 需要管理员权限。请先退出助手，再从开始菜单用当前账号以管理员身份运行。"
	}
	ui.setNotice(ui.tunHint, tunHelp, ui.colors.muted)

	updatedLabel, refreshHint := "", ""
	if ui.state.Subscription {
		lastUpdate := "尚未更新"
		if updatedAt := ui.state.SubscriptionUpdatedAt; !updatedAt.IsZero() {
			lastUpdate = updatedAt.Local().Format("2006-01-02 15:04:05")
		}
		updatedLabel = "最近更新时间 " + lastUpdate
		if active {
			refreshHint = "订阅每小时自动刷新。"
		}
		if checkedAt := ui.state.SubscriptionCheckedAt; ui.state.SubscriptionRefreshFailed && !checkedAt.IsZero() {
			refreshHint = "订阅刷新失败（" + checkedAt.Local().Format("01-02 15:04") + "）。"
			if active {
				refreshHint += "将在下一轮自动重试。"
			}
		}
	}
	ui.setNotice(ui.proxyUpdatedAt, updatedLabel, ui.colors.muted)
	refreshColor := ui.colors.muted
	if ui.state.SubscriptionRefreshFailed {
		refreshColor = ui.colors.danger
	}
	ui.setNotice(ui.subscriptionHint, refreshHint, refreshColor)
	ui.proxyModes.SetEnabled(idle)
	ui.proxy.SetText(proxyAction(selected, current))
	ui.proxy.SetEnabled(idle && (selected == current || ui.state.Subscription))
}

func (ui *nativeWindow) toggleProxy() {
	mode, current := ui.selectedProxyMode(), ui.state.ProxyMode
	on := mode != current
	action := proxyAction(mode, current)
	ui.runTask(ui.proxyNotice, "正在"+action+"…", func(context.Context) error {
		return ui.controller.SetProxyMode(on, mode)
	}, func(err error) {
		if err != nil {
			if mode != proxy.ModeTUN && current != proxy.ModeTUN {
				ui.setNotice(ui.proxyNotice, err.Error(), ui.colors.danger)
				return
			}
			ui.setNotice(ui.proxyNotice, "", ui.colors.muted)
			walk.MsgBox(ui.window, "TUN 操作未完成", err.Error(), walk.MsgBoxOK|walk.MsgBoxIconWarning)
			return
		}
		ui.setNotice(ui.proxyNotice, "", ui.colors.muted)
	})
}
