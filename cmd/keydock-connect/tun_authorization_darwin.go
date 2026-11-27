//go:build darwin && arm64 && cgo

package main

/*
#cgo LDFLAGS: -framework ServiceManagement
#include "window_darwin.h"
#include <stdbool.h>
#include <stdlib.h>
int kdProxyServiceStatus(void);
void kdOpenProxyApproval(void);
char *kdRegisterProxyService(bool replace);
*/
import "C"

import (
	"context"
	"errors"
	"time"
	"unsafe"

	"github.com/yungor-ai/yungor-keydock-connect/internal/proxy"
)

func registerDarwinProxyService(replace bool) error {
	message := C.kdRegisterProxyService(C.bool(replace))
	if message == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(message))
	return errors.New(C.GoString(message))
}

func authorizeDarwinProxyService(ctx context.Context) error {
	status := C.kdProxyServiceStatus()
	switch status {
	case 0, 3:
		if err := registerDarwinProxyService(false); err != nil {
			return err
		}
	case 1, 2:
	default:
		return errors.New("无法读取 macOS 后台项目状态")
	}
	if err := awaitDarwinProxyApproval(ctx); err != nil {
		return err
	}
	release, err := proxy.PrepareDarwinRegistration(ctx)
	if err != nil || release == nil {
		return err
	}
	defer release()
	if err := registerDarwinProxyService(true); err != nil {
		return err
	}
	if err := awaitDarwinProxyApproval(ctx); err != nil {
		return err
	}
	// Never report an enabled service by SMAppService status alone: a stale
	// launchd job can still have the same label and approved signing identity.
	again, err := proxy.PrepareDarwinRegistration(ctx)
	if again != nil {
		again()
		return errors.New("macOS 仍在运行旧版后台服务，请稍后重新启用代理")
	}
	return err
}

func awaitDarwinProxyApproval(ctx context.Context) error {
	if C.kdProxyServiceStatus() == 1 {
		return nil
	}
	if C.kdProxyServiceStatus() != 2 {
		return errors.New("macOS 未启用网络后台服务，请从「应用程序」打开 KeyDock Connect 后重试")
	}
	C.kdSetProxyApprovalProgress(1)
	C.kdOpenProxyApproval()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return errors.New("请在系统设置的后台项目中允许 KeyDock Connect，随后重新启用代理")
		case <-ticker.C:
			if C.kdProxyServiceStatus() == 1 {
				C.kdSetProxyApprovalProgress(0)
				return nil
			}
		}
	}
}
