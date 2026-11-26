//go:build windows

package main

import (
	"context"

	"github.com/tailscale/win"
	"github.com/yungor-ai/yungor-keydock-connect/internal/proxy"
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
	title, hint := "系统代理未启用", "启用后，使用系统代理的应用流量将按订阅规则分流。"
	switch {
	case current == proxy.ModeTUN:
		title, hint = "TUN 已启用", "流量正通过虚拟网卡按订阅规则分流。"
	case active:
		title, hint = "系统代理已启用", "使用系统代理的应用流量正按订阅规则分流。"
	case selected == proxy.ModeTUN:
		title, hint = "TUN 未启用", "通过虚拟网卡按订阅规则分流，覆盖绕过系统代理的应用。"
	}
	ui.proxyState.SetText(title)
	if active {
		ui.proxyState.SetTextColor(ui.colors.success)
		hint += " 每小时自动刷新订阅。"
	} else {
		ui.proxyState.SetTextColor(ui.colors.ink)
		if !ui.state.Subscription {
			unavailable := "连接设备并获取 SS 订阅后，可在此启用。"
			if ui.state.Paired {
				unavailable = "当前设备未分配 SS 订阅。"
			}
			if selected == proxy.ModeTUN {
				hint += " " + unavailable
			} else {
				hint = unavailable
			}
		}
	}
	if checkedAt := ui.state.SubscriptionCheckedAt; ui.state.Subscription && ui.state.SubscriptionRefreshFailed && !checkedAt.IsZero() {
		stamp := checkedAt.Local().Format("01-02 15:04")
		hint += " 最近刷新失败（" + stamp + "）。"
		if active {
			hint += "将在下一轮自动重试。"
		}
	}
	visibility := int32(win.SW_HIDE)
	if ui.state.Subscription {
		lastUpdate := "尚未更新"
		if updatedAt := ui.state.SubscriptionUpdatedAt; !updatedAt.IsZero() {
			lastUpdate = updatedAt.Local().Format("2006-01-02 15:04:05")
		}
		ui.proxyUpdatedAt.SetText("最近更新时间 " + lastUpdate)
		visibility = win.SW_SHOWNA
	}
	// Set the label's own visibility so refreshes work while hidden in the tray.
	win.ShowWindow(ui.proxyUpdatedAt.Handle(), visibility)
	ui.proxyHint.SetText(hint)
	ui.proxyModes.SetEnabled(idle)
	ui.proxy.SetText(proxyAction(selected, current))
	ui.proxy.SetEnabled(idle && (selected == current || ui.state.Subscription))
}

func (ui *nativeWindow) toggleProxy() {
	mode, current := ui.selectedProxyMode(), ui.state.ProxyMode
	on := mode != current
	action := proxyAction(mode, current)
	ui.runAction("正在"+action+"…", "已"+action, func(context.Context) error {
		return ui.controller.SetProxyMode(on, mode)
	})
}
