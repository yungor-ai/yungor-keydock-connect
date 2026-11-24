package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/yungor-ai/yungor-keydock-connect/internal/codex"
	"github.com/yungor-ai/yungor-keydock-connect/internal/desktop"
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
		return errors.New("KeyDock 接入助手目前支持 Windows x64")
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
	endpoint := filepath.Join(stateDir, "window.bin")
	if !locked {
		if uninstall {
			return errors.New("请先从窗口退出 KeyDock 接入助手")
		}
		// The first invocation may still be opening the loopback listener.
		for i := 0; i < 20; i++ {
			if saved, err := os.ReadFile(endpoint); err == nil {
				if plain, err := codex.Unprotect(saved); err == nil && validWindowURL(string(plain)) {
					return browse(string(plain))
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		return errors.New("接入助手正在启动，请稍后再次打开")
	}
	if err = os.Remove(endpoint); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
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
	mgr := proxy.New(stateDir, filepath.Join(filepath.Dir(exe), "core", "mihomo.exe"))
	if err = mgr.Recover(); err != nil {
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
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	app, err := desktop.New(listener.Addr().String(), home, stateDir, mgr, desktop.SystemBackend(), cancel)
	if err != nil {
		return err
	}
	secret, err := codex.Protect([]byte(app.URL()))
	if err != nil {
		return err
	}
	if err = desktop.WritePrivate(endpoint, secret); err != nil {
		return err
	}
	defer os.Remove(endpoint)
	server := &http.Server{Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 90 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	failed := make(chan error, 1)
	go func() { failed <- server.Serve(listener) }()
	if err = browse(app.URL()); err != nil {
		_ = server.Close()
		return err
	}
	select {
	case <-ctx.Done():
	case err = <-failed:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	drain, finish := context.WithTimeout(context.Background(), 12*time.Second)
	defer finish()
	_ = server.Shutdown(drain)
	return mgr.Stop()
}
