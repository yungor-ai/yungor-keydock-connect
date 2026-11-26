//go:build !windows && !darwin

package desktop

import "errors"

func launchCodex() error { return errors.New("当前平台没有可用的桌面集成") }
