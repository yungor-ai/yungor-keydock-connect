//go:build windows

package main

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"unsafe"
)

const supported = true

func lockInstance(dir string) (*os.File, bool, error) {
	f, err := os.OpenFile(filepath.Join(dir, "window.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, err
	}
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return f, false, nil
	}
	if err != nil {
		f.Close()
		return nil, false, err
	}
	return f, true, nil
}
func uninstalling() bool {
	name, _ := windows.UTF16PtrFromString(`Local\Yungor.KeyDockConnect.Uninstalling`)
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err == nil {
		windows.CloseHandle(h)
		return true
	}
	return !errors.Is(err, windows.ERROR_FILE_NOT_FOUND)
}
func displayError(s string) {
	p, _ := windows.UTF16PtrFromString(s)
	t, _ := windows.UTF16PtrFromString("KeyDock 接入助手")
	windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(t)), 0x10)
}
