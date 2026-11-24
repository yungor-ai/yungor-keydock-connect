//go:build windows

package main

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
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
func browse(raw string) error {
	// Edge app mode provides a dedicated window without requiring another runtime.
	for _, root := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles")} {
		if root == "" {
			continue
		}
		path := filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe")
		if _, err := os.Stat(path); err == nil {
			args := []string{"--app=" + raw, "--no-first-run"}
			if local := os.Getenv("LOCALAPPDATA"); local != "" {
				args = append(args, "--user-data-dir="+filepath.Join(local, "Yungor", "KeyDockConnect", "Window"))
			}
			cmd := exec.Command(path, args...)
			if err := cmd.Start(); err == nil {
				return cmd.Process.Release()
			}
		}
	}

	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", raw)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
func displayError(s string) {
	p, _ := windows.UTF16PtrFromString(s)
	t, _ := windows.UTF16PtrFromString("KeyDock 接入助手")
	windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW").Call(0, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(t)), 0x10)
}
