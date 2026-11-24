//go:build windows

package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

func activationName(stateDir string) *uint16 {
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(stateDir))))
	name, _ := windows.UTF16PtrFromString(fmt.Sprintf(`Local\Yungor.KeyDockConnect.Show.%x`, digest[:16]))
	return name
}

func reopenWindow(stateDir string) error {
	// The first invocation holds the instance lock while recovering its
	// networking state and creating the native message loop.
	for i := 0; i < 80; i++ {
		event, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, activationName(stateDir))
		if err == nil {
			defer windows.CloseHandle(event)
			return windows.SetEvent(event)
		}
		if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("窗口尚未就绪，请稍后从系统托盘打开 KeyDock Connect")
}

// Wait on two kernel events so a second launch can show the first window
// without a listening socket, and shutdown can join the waiter before cleanup.
func watchActivation(stateDir string, show func()) (func(), error) {
	event, err := windows.CreateEvent(nil, 0, 0, activationName(stateDir))
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, err
	}
	stop, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(event)
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			n, err := windows.WaitForMultipleObjects([]windows.Handle{stop, event}, false, windows.INFINITE)
			if err != nil || n != windows.WAIT_OBJECT_0+1 {
				return
			}
			show()
		}
	}()
	return func() {
		windows.SetEvent(stop)
		<-done
		windows.CloseHandle(stop)
		windows.CloseHandle(event)
	}, nil
}
