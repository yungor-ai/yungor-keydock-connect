//go:generate go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -manifest keydock.manifest -ico keydock.ico -o resources_windows_amd64.syso

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/yungor-ai/yungor-keydock-connect/internal/codex"
	"github.com/yungor-ai/yungor-keydock-connect/internal/desktop"
	"github.com/yungor-ai/yungor-keydock-connect/internal/diagnostics"
	"github.com/yungor-ai/yungor-keydock-connect/internal/proxy"
)

func main() {
	err := run(os.Args[1:])
	if err != nil {
		// The credential subprocess speaks stdout only on success and never shows a dialog.
		if len(os.Args) > 1 {
			fmt.Fprintln(os.Stderr, err)
		} else {
			displayError(err.Error())
		}
		os.Exit(1)
	}
}

func run(args []string) error {
	if handled, err := proxy.RunWorker(args); handled {
		return err
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	stateDir := filepath.Join(dir, "Yungor", "KeyDockConnect")
	if len(args) == 1 && args[0] == "--credential" {
		key, err := codex.Credential(stateDir)
		if err != nil {
			return errors.New("凭据暂不可用")
		}
		_, err = fmt.Fprintln(os.Stdout, key)
		return err
	}
	uninstall := len(args) == 1 && args[0] == "--prepare-uninstall"
	if len(args) > 0 && !uninstall {
		return errors.New("未知启动参数")
	}
	if !supported {
		return errors.New("KeyDock 接入助手支持 Windows x64 和 Apple Silicon Mac")
	}
	if uninstall {
		if err := checkUninstallUser(); err != nil {
			return err
		}
	}
	if err = os.MkdirAll(stateDir, 0700); err != nil {
		return err
	}
	guard, locked, err := lockInstance(stateDir)
	if err != nil {
		return err
	}
	defer guard.Close()
	if !uninstall && uninstalling() {
		return errors.New("卸载正在进行，请等待卸载完成")
	}
	if !locked {
		if uninstall {
			if runtime.GOOS == "windows" {
				return errors.New("请先从系统托盘退出 KeyDock Connect")
			}
			return errors.New("请先退出 KeyDock Connect")
		}
		return reopenWindow(stateDir)
	}
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		home = filepath.Join(user, ".codex")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	corePath := filepath.Join(filepath.Dir(exe), "core", "mihomo.exe")
	if runtime.GOOS == "darwin" {
		corePath = filepath.Join(filepath.Dir(exe), "..", "Resources", "core", "mihomo")
	}
	mgr := proxy.New(stateDir, corePath)
	var log *diagnostics.Log
	if !uninstall {
		log = diagnostics.New(stateDir)
		mgr.SetDiagnostics(log)
		log.Record("app.start", "ok", 0, nil, 0)
	}
	start := time.Now()
	err = mgr.Recover()
	log.RecordResult("proxy.recover", start, err)
	if err != nil {
		return err
	}
	if uninstall {
		if err = codex.Restore(home, stateDir); err != nil {
			return err
		}
		if err = os.Remove(filepath.Join(stateDir, "device.bin")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	start = time.Now()
	controller, err := desktop.NewController(home, stateDir, mgr, desktop.SystemBackend(stateDir), log)
	log.RecordResult("app.enrollment_load", start, err)
	if err != nil {
		return err
	}
	err = runWindow(stateDir, controller)
	if err != nil {
		log.Record("app.window", "failed", 0, err, 0)
	}
	return err
}
