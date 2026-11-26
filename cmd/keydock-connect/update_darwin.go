//go:build darwin && arm64 && cgo

package main

/*
#include <stdlib.h>
#include "window_darwin.h"
*/
import "C"

import (
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	keydockconnect "github.com/yungor-ai/yungor-keydock-connect"
	"github.com/yungor-ai/yungor-keydock-connect/internal/selfupdate"
)

type macUpdate struct {
	mu      sync.Mutex
	release *selfupdate.Release
	install *selfupdate.MacInstall
}

func (u *macUpdate) takeInstall() *selfupdate.MacInstall {
	u.mu.Lock()
	defer u.mu.Unlock()
	prepared := u.install
	u.install, u.release = nil, nil
	return prepared
}

//export kdMacSoftwareURL
func kdMacSoftwareURL(server *C.char) *C.char {
	url, err := selfupdate.SoftwareURL(C.GoString(server))
	if err != nil {
		return nil
	}
	return C.CString(url)
}

//export kdMacCheckUpdate
func kdMacCheckUpdate(server *C.char) {
	serverURL := C.GoString(server)
	current := window
	ctx := current.actionLifetime
	current.actions.Add(1)
	go func() {
		defer current.actions.Done()
		release, err := selfupdate.Check(ctx, serverURL)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			finishMacOperation("检查更新失败："+err.Error(), true)
			return
		}
		if release == nil {
			finishMacOperation("当前版本 v"+keydockconnect.Version()+" 无需更新。", false)
			return
		}
		current.update.mu.Lock()
		current.update.release = release
		current.update.mu.Unlock()
		version := C.CString(release.Version)
		defer C.free(unsafe.Pointer(version))
		C.kdOfferUpdate(version)
	}()
}

//export kdMacUpdateDecision
func kdMacUpdateDecision(accepted C.int) {
	current := window
	current.update.mu.Lock()
	release := current.update.release
	current.update.release = nil
	current.update.mu.Unlock()
	if accepted == 0 {
		return
	}
	if release == nil {
		finishMacOperation("请重新检查更新。", true)
		return
	}
	ctx := current.actionLifetime
	current.actions.Add(1)
	go func() {
		defer current.actions.Done()
		if ctx.Err() != nil {
			return
		}
		if err := selfupdate.CheckMacOSInstall(); err != nil {
			finishMacOperation(err.Error(), true)
			return
		}
		path, err := selfupdate.Download(ctx, release)
		if err != nil {
			if ctx.Err() == nil {
				finishMacOperation("下载更新失败："+err.Error(), true)
			}
			return
		}
		defer os.RemoveAll(filepath.Dir(path))
		if ctx.Err() != nil {
			return
		}
		message := C.CString("正在准备安装…")
		C.kdSetUpdateProgress(message)
		C.free(unsafe.Pointer(message))
		prepared, err := selfupdate.PrepareMacOS(ctx, path, release.Version)
		if err != nil {
			if ctx.Err() == nil {
				finishMacOperation("准备更新失败："+err.Error(), true)
			}
			return
		}
		if ctx.Err() != nil {
			prepared.Discard()
			return
		}
		current.update.mu.Lock()
		current.update.install = prepared
		current.update.mu.Unlock()
		C.kdRequestUpdateTermination()
	}()
}
