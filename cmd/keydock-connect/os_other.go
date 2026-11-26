//go:build !windows && !(darwin && arm64 && cgo)

package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/yungor-ai/yungor-keydock-connect/internal/desktop"
)

const supported = false

func lockInstance(dir string) (*os.File, bool, error) {
	return nil, false, errors.New("当前平台没有可用的桌面集成")
}
func reopenWindow(string) error { return errors.New("当前平台没有可用的桌面集成") }
func runWindow(string, *desktop.Controller) error {
	return errors.New("当前平台没有可用的桌面集成")
}
func displayError(s string) { fmt.Fprintln(os.Stderr, s) }

func uninstalling() bool { return false }
