package proxy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// TUN elevates the pinned core. Only offer it from the fixed administrator-
// managed installation tree, rather than elevating a per-user program directory.
func checkProtectedTUNInstall(corePath string) error {
	unprotected := errors.New("TUN 需要从 Program Files 下的 KeyDock Connect 目录运行。请让管理员将完整程序放入该目录")
	programFiles, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return unprotected
	}
	programFiles, err = filepath.EvalSymlinks(programFiles)
	if err != nil {
		return unprotected
	}
	exe, err := os.Executable()
	if err != nil {
		return unprotected
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return unprotected
	}
	core, err := filepath.EvalSymlinks(corePath)
	dir := filepath.Join(programFiles, "KeyDock Connect")
	if err != nil || !strings.EqualFold(filepath.Dir(exe), dir) || !strings.EqualFold(core, filepath.Join(dir, "core", "mihomo.exe")) {
		return unprotected
	}
	return nil
}
