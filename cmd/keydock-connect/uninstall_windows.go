package main

import (
	"errors"

	"golang.org/x/sys/windows"
)

func checkUninstallUser() error {
	// A UAC prompt can run the uninstaller as a different Windows user.
	// Its DPAPI and Windows proxy access must belong to the desktop user.
	wrongUser := errors.New("请在管理员自己的 Windows 会话中卸载 KeyDock Connect")
	shell := windows.GetShellWindow()
	if shell == 0 {
		return wrongUser
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(shell, &pid); err != nil {
		return wrongUser
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return wrongUser
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err = windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return wrongUser
	}
	defer token.Close()
	desktopUser, err := token.GetTokenUser()
	if err != nil {
		return wrongUser
	}
	currentUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || !desktopUser.User.Sid.Equals(currentUser.User.Sid) {
		return wrongUser
	}
	return nil
}
