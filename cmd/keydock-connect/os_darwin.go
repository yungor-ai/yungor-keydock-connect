//go:build darwin && arm64 && cgo

package main

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework AppKit -framework UniformTypeIdentifiers
#include <stdlib.h>
#include "window_darwin.h"
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/yungor-ai/yungor-keydock-connect/internal/desktop"
	"github.com/yungor-ai/yungor-keydock-connect/internal/proxy"
	"golang.org/x/sys/unix"
)

const supported = true

// AppKit must start on the OS main thread.
func init() { runtime.LockOSThread() }

func lockInstance(dir string) (*os.File, bool, error) {
	file, err := os.OpenFile(filepath.Join(dir, "window.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return file, false, nil
	}
	if err != nil {
		file.Close()
		return nil, false, err
	}
	return file, true, nil
}

func uninstalling() bool { return false }

func reopenWindow(string) error {
	C.kdReopenWindow()
	return nil
}

func displayError(message string) {
	value := C.CString(message)
	defer C.free(unsafe.Pointer(value))
	C.kdShowError(value)
}

type macWindow struct {
	controller     *desktop.Controller
	lifetime       context.Context
	stop           context.CancelFunc
	actionLifetime context.Context
	stopActions    context.CancelFunc
	actions        sync.WaitGroup
	update         macUpdate
}

// Cocoa calls the exported functions on its main thread. Workers only publish
// updates through functions that dispatch back onto the main queue.
var window *macWindow

func runWindow(_ string, controller *desktop.Controller) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	window = &macWindow{controller: controller, lifetime: ctx, stop: cancel}
	kdMacResumeActions()
	go controller.RefreshSubscriptions(ctx, func() { publishMacState(controller.State(), false) })
	C.kdRunWindow()
	return controller.Close()
}

func publishMacState(state desktop.State, initial bool) {
	var server *C.char
	if initial {
		server = C.CString(state.ServerURL)
		defer C.free(unsafe.Pointer(server))
	}
	device := C.CString(state.Device)
	model := C.CString(state.Model)
	updatedAt := C.CString(formatMacSubscriptionTime(state.SubscriptionUpdatedAt))
	defer C.free(unsafe.Pointer(device))
	defer C.free(unsafe.Pointer(model))
	defer C.free(unsafe.Pointer(updatedAt))
	C.kdSetState(server, device, model, cFlag(state.Paired),
		cFlag(state.Restorable), cFlag(state.Subscription), cProxyMode(state.ProxyMode), updatedAt, cFlag(state.SubscriptionRefreshFailed))
}

func cProxyMode(mode proxy.Mode) C.int {
	switch mode {
	case proxy.ModeSystem:
		return C.KD_PROXY_SYSTEM
	case proxy.ModeTUN:
		return C.KD_PROXY_TUN
	default:
		return C.KD_PROXY_INACTIVE
	}
}

func formatMacSubscriptionTime(updatedAt time.Time) string {
	if updatedAt.IsZero() {
		return ""
	}
	return updatedAt.Local().Format("2006-01-02 15:04:05")
}

func cFlag(value bool) C.int {
	if value {
		return 1
	}
	return 0
}

func publishMacCheck(check desktop.Check) {
	var row C.int
	switch check.Stage {
	case "device":
		row = C.KD_CHECK_DEVICE
	case "codex":
		row = C.KD_CHECK_CODEX
	case "model":
		row = C.KD_CHECK_MODEL
	case "proxy":
		row = C.KD_CHECK_PROXY
	case "google":
		row = C.KD_CHECK_GOOGLE
	case "egress":
		row = C.KD_CHECK_EGRESS
	default:
		return
	}
	status := C.CString(check.Status)
	message := C.CString(check.Message)
	hint := C.CString(check.Hint)
	defer C.free(unsafe.Pointer(status))
	defer C.free(unsafe.Pointer(message))
	defer C.free(unsafe.Pointer(hint))
	C.kdSetCheck(row, status, message, hint)
}

//export kdMacReady
func kdMacReady() {
	current := window
	go func() { publishMacState(current.controller.State(), true) }()
	go current.controller.WatchNetwork(current.lifetime, publishMacCheck)
}

//export kdMacRecheckNetwork
func kdMacRecheckNetwork() {
	window.controller.RecheckNetwork()
}

//export kdMacProxyMode
func kdMacProxyMode() C.int { return cProxyMode(window.controller.ActiveProxyMode()) }

//export kdMacAction
func kdMacAction(action C.int, first, second *C.char) {
	firstValue, secondValue := C.GoString(first), C.GoString(second)
	current := window
	ctx := current.actionLifetime
	current.actions.Add(1)
	go func() {
		defer current.actions.Done()
		if ctx.Err() != nil {
			return
		}
		controller := current.controller
		var err error
		var message string
		var deviceFailure string
		switch action {
		case C.KD_CONNECT:
			err = controller.Connect(ctx, firstValue, secondValue, func(check desktop.Check) {
				if check.Stage == "device" && check.Status == "failed" {
					deviceFailure = check.Message
				}
				publishMacCheck(check)
			})
			message = "连接检查已完成，可在下方查看分项结果。"
		case C.KD_PROXY:
			enable := firstValue == "on"
			mode := proxy.Mode(secondValue)
			message, err = setMacProxyMode(controller, enable, mode)
		case C.KD_RESTORE:
			err = controller.Restore()
			message = "已恢复原 Codex 配置"
		case C.KD_EXPORT:
			err = controller.ExportDiagnostics(firstValue)
			message = "已导出诊断日志"
		}
		if err != nil {
			message = err.Error()
			if action == C.KD_CONNECT {
				message = "部分检查未通过，请查看下方「连接检查」。"
				if deviceFailure != "" {
					message = deviceFailure
					var status interface{ HTTPStatus() int }
					if errors.As(err, &status) && (status.HTTPStatus() == http.StatusUnauthorized || status.HTTPStatus() == http.StatusForbidden) {
						message += "。请核对服务器地址和设备密钥后重试。"
					}
				}
			}
		}
		publishMacState(controller.State(), false)
		finishMacOperation(message, err != nil)
	}()
}

func finishMacOperation(message string, failed bool) {
	value := C.CString(message)
	defer C.free(unsafe.Pointer(value))
	C.kdFinishOperation(value, cFlag(failed))
}

//export kdMacResumeActions
func kdMacResumeActions() {
	window.actionLifetime, window.stopActions = context.WithCancel(window.lifetime)
}

func setMacProxyMode(controller *desktop.Controller, enable bool, mode proxy.Mode) (string, error) {
	var label string
	switch mode {
	case proxy.ModeSystem:
		label = "系统代理"
	case proxy.ModeTUN:
		label = "TUN 虚拟网卡"
	default:
		return "", errors.New("无效的代理模式")
	}
	if enable {
		if err := authorizeDarwinProxyService(); err != nil {
			return "", err
		}
	}
	if err := controller.SetProxyMode(enable, mode); err != nil {
		return "", err
	}
	if enable {
		return label + "已启用", nil
	}
	return label + "已停用", nil
}

//export kdMacQuit
func kdMacQuit(applyUpdate C.int) {
	current := window
	current.stopActions()
	go func() {
		current.actions.Wait()
		prepared := current.update.takeInstall()
		err := current.controller.Close()
		if err == nil && applyUpdate != 0 {
			if prepared == nil {
				err = errors.New("更新尚未准备完成，请重新检查更新。")
			} else if installErr := prepared.Install(); installErr != nil {
				err = fmt.Errorf("无法安装更新：%w", installErr)
			}
		}
		if prepared != nil {
			prepared.Discard()
		}
		if err != nil {
			message := C.CString(err.Error())
			defer C.free(unsafe.Pointer(message))
			C.kdFinishTermination(message)
			publishMacState(current.controller.State(), false)
			return
		}
		current.stop()
		C.kdFinishTermination(nil)
	}()
}
