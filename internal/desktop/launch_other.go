//go:build !windows

package desktop

import "errors"

func launchCodex() error { return errors.New("本版本的系统集成适用于原生 Windows") }
