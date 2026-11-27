//go:build darwin && arm64 && cgo

package proxy

/*
int kdSystemProxyUpdate(int enable, int port);
*/
import "C"

import (
	"context"
	"errors"
	"time"
)

var errNoDarwinProxyService = errors.New("没有可设置系统代理的活动 IPv4 网络，请连接 Wi-Fi 或以太网后重试")

func updateDarwinSystemProxy(enabled bool) error {
	var enable C.int
	if enabled {
		enable = 1
	}
	// Another writer can briefly own the preferences lock (or change the
	// preferences signature between the read and lock). Use a fresh session.
	status := C.kdSystemProxyUpdate(enable, localPort)
	for deadline := time.Now().Add(2 * time.Second); status == 2 && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
		status = C.kdSystemProxyUpdate(enable, localPort)
	}
	switch status {
	case 0:
		return nil
	case 1:
		return errNoDarwinProxyService
	case 2:
		return errors.New("macOS 网络设置正在使用或无法访问，请稍后重试")
	case 3:
		return errors.New("macOS 尚未恢复原系统代理设置，请稍后重试")
	case 5:
		return errors.New("系统代理设置已被更改，KeyDock 已停止接管")
	default:
		return errors.New("macOS 未能启用 HTTP、HTTPS 和 SOCKS 系统代理，请检查当前网络服务后重试")
	}
}

func runDarwinSystemProxy(ctx context.Context, ready func() error) (result error) {
	defer func() { result = errors.Join(result, updateDarwinSystemProxy(false)) }()
	if err := updateDarwinSystemProxy(true); err != nil {
		return err
	}
	if err := ready(); err != nil {
		return err
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Restore the previous service on disconnect and follow the new
			// primary IPv4 service when a Wi-Fi or Ethernet link is ready.
			if err := updateDarwinSystemProxy(true); err != nil && !errors.Is(err, errNoDarwinProxyService) {
				return err
			}
		}
	}
}
