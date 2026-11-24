//go:build !windows

package main

import (
	"errors"
	"fmt"
	"github.com/yungor-ai/yungor-keydock-connect/internal/desktop"
	"os"
)

const supported = false

func lockInstance(dir string) (*os.File, bool, error) {
	return nil, false, errors.New("需要 Windows")
}
func reopenWindow(string) error                   { return errors.New("需要 Windows") }
func runWindow(string, *desktop.Controller) error { return errors.New("需要 Windows") }
func displayError(s string)                       { fmt.Fprintln(os.Stderr, s) }

func uninstalling() bool { return false }
