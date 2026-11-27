//go:build darwin && arm64 && cgo

package proxy

import (
	"context"
	"errors"
	"time"

	connect "github.com/yungor-ai/yungor-keydock-connect"
)

const darwinRegisterRequest = "KeyDock-Reserve-Registration/1"

func serveRegisteredDarwinService(ctx context.Context, args []string, corePath string, body []byte, ready func() error) error {
	current := len(args) == 2 && args[0] == "--tun-daemon" && args[1] == connect.Version()
	if string(body) != darwinRegisterRequest {
		if !current {
			return errors.New("网络后台服务仍使用旧版注册信息，请从新版 KeyDock Connect 启用代理")
		}
		return runDarwinNetworkService(ctx, corePath, body, ready)
	}
	if current {
		return errors.New("后台服务已经更新，请重新启用代理")
	}
	// The caller can only unregister while holding this ordinary signed lease.
	// No network configuration or TUN is touched by a registration reservation.
	if err := ready(); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

// PrepareDarwinRegistration checks the running job's signed bundle and its
// launchd-supplied build version. A non-nil release function means the idle,
// outdated registration has been reserved and can now be replaced.
func PrepareDarwinRegistration(ctx context.Context) (func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return prepareDarwinRegistration(ctx, probeDarwinRegistration, func() (func(), error) {
		lease, err := openDarwinTUN(ctx, []byte(darwinRegisterRequest))
		if err != nil {
			return nil, err
		}
		return func() { _ = lease.conn.Close() }, nil
	}, 300*time.Millisecond)
}

func prepareDarwinRegistration(ctx context.Context, probe func() int, reserve func() (func(), error), interval time.Duration) (func(), error) {
	for {
		switch probe() {
		case 1: // Same installation, current registration.
			return nil, nil
		case 2: // Same installation, outdated launchd arguments.
			return reserve()
		case 4:
			return nil, errors.New("另一个位置的 KeyDock Connect 已注册后台服务，请退出其他副本后在「应用程序」中替换旧版")
		}
		// A live v0.4.6 daemon already retires itself when idle after its
		// executable is replaced. A failed probe does not prove it is idle.
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, ctx.Err()
			}
			return nil, errors.New("无法确认旧后台服务已空闲。请退出所有登录用户的 KeyDock Connect，将新版替换到「应用程序」，重新启动 Mac 后再启用代理")
		case <-time.After(interval):
		}
	}
}
