package desktop

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

func launchCodex() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "/usr/bin/open", "-b", "com.openai.codex").Run(); err != nil {
		return errors.New("未能打开 Codex 桌面版。请在「应用程序」中打开 Codex，或在新终端运行 codex。")
	}
	return nil
}
