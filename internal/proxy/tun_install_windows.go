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
	unprotected := errors.New("请从 KeyDock 的「软件下载」页下载 Windows 安装包，退出托盘中的助手后安装，再从开始菜单打开 KeyDock Connect")
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
