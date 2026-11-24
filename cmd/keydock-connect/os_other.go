//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
)

const supported = false

func lockInstance(dir string) (*os.File, bool, error) {
	return nil, false, errors.New("需要 Windows")
}
func browse(raw string) error { return errors.New("需要 Windows") }
func displayError(s string)   { fmt.Fprintln(os.Stderr, s) }

func uninstalling() bool { return false }
