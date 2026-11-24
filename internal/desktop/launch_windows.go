//go:build windows

package desktop

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func launchCodex() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Resolve the registered Start menu entry so Microsoft Store upgrades do not invalidate a path.
	const script = `$ErrorActionPreference='Stop'; $app=Get-StartApps | Where-Object { $_.Name -eq 'Codex' -or $_.AppID -like 'OpenAI.Codex*!*' } | Select-Object -First 1; if (-not $app) { exit 2 }; Start-Process explorer.exe -ArgumentList ('shell:AppsFolder\'+$app.AppID)`
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		return errors.New("未能打开 Codex 桌面版。请从开始菜单打开，或参阅官方安装指南。使用 CLI 时可在新终端运行 codex。")
	}
	return nil
}
