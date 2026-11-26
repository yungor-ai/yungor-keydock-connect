//go:build !windows

package codex

import (
	"os"

	"golang.org/x/sys/unix"
)

func lock(f *os.File) error         { return unix.Flock(int(f.Fd()), unix.LOCK_EX) }
func replace(old, new string) error { return os.Rename(old, new) }
