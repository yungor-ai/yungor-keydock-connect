//go:build !windows

package codex

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var unsupported = errors.New("凭据存储仅支持 Windows DPAPI")

func Protect([]byte) ([]byte, error)   { return nil, unsupported }
func Unprotect([]byte) ([]byte, error) { return nil, unsupported }
func lock(f *os.File) error            { return unix.Flock(int(f.Fd()), unix.LOCK_EX) }
func replace(old, new string) error    { return os.Rename(old, new) }
