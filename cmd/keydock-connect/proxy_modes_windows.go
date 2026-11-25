//go:build windows

package main

import (
	"context"

	"github.com/tailscale/win"
	"github.com/yungor-ai/yungor-keydock-connect/internal/proxy"
)

const tunModeIndex = 1

func (ui *nativeWindow) selectedProxyMode() proxy.Mode {
	if ui.state.Proxy {
		return ui.state.ProxyMode
	}
	if ui.proxyModes.CurrentIndex() == tunModeIndex {
		return proxy.ModeTUN
	}
	return proxy.ModeSystem
}

func (ui *nativeWindow) renderProxy() {
	mode := ui.selectedProxyMode()
	active := ui.state.Proxy
	idle := ui.cancel == nil && !ui.quitting
	ready := ui.state.Subscription
	var name, hint string
	if mode == proxy.ModeTUN {
		name = "TUN"
		hint = "通过虚拟网卡按订阅规则分流，覆盖绕过系统代理的应用。"
	} else {
		name = "系统代理"
		hint = "启用后，使用系统代理的应用流量将按订阅规则分流。"
	}
	if active {
		index := 0
		if mode == proxy.ModeTUN {
			index = tunModeIndex
		} else {
			hint = "使用系统代理的应用流量正按订阅规则分流。"
		}
		ui.proxyModes.SetCurrentIndex(index)
		ui.proxy.SetText("关闭" + name)
		ui.proxyState.SetText(name + "已启用")
		ui.proxyState.SetTextColor(ui.colors.success)
		hint += " 每小时自动刷新订阅。"
	} else {
		ui.proxy.SetText("启用" + name)
		ui.proxyState.SetText(name + "未启用")
		ui.proxyState.SetTextColor(ui.colors.ink)
		if !ready {
			unavailable := "连接设备并获取 SS 订阅后，可在此启用。"
			if ui.state.Paired {
				unavailable = "当前设备未分配 SS 订阅。"
			}
			if mode == proxy.ModeTUN {
				hint += " " + unavailable
			} else {
				hint = unavailable
			}
		}
		if mode == proxy.ModeTUN {
			if err := ui.controller.CheckProxyMode(mode); err != nil {
				ready = false
				hint += " " + err.Error()
			} else {
				hint += " TUN 需要管理员权限。"
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
	ui.proxyModes.SetEnabled(idle && !active)
	ui.proxy.SetEnabled(idle && (active || ready))
}

func (ui *nativeWindow) toggleProxy() {
	on, mode := !ui.state.Proxy, ui.selectedProxyMode()
	progress, done := "正在更新系统代理…", "已更新 Windows 代理设置"
	if mode == proxy.ModeTUN {
		progress, done = "正在更新 TUN…", "已更新 TUN（虚拟网卡）设置"
	}
	ui.runAction(progress, done, func(context.Context) error {
		return ui.controller.SetProxyMode(on, mode)
	})
}
