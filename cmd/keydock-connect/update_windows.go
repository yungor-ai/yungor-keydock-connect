//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"unsafe"

	"github.com/tailscale/walk"
	d "github.com/tailscale/walk/declarative"
	"github.com/tailscale/win"
	"github.com/yungor-ai/yungor-keydock-connect/internal/selfupdate"
	"golang.org/x/sys/windows"
)

func (ui *nativeWindow) openSoftware() {
	url, err := selfupdate.SoftwareURL(ui.server.Text())
	if err != nil {
		ui.setMessage(err.Error(), ui.colors.danger)
		return
	}
	if err := windows.ShellExecute(windows.Handle(ui.window.Handle()), nil, windows.StringToUTF16Ptr(url), nil, nil, win.SW_SHOWNORMAL); err != nil {
		ui.setMessage("无法在浏览器打开软件下载页。", ui.colors.danger)
	}
}

func (ui *nativeWindow) checkForUpdate() {
	server := ui.server.Text()
	var release *selfupdate.Release
	ui.runTask(ui.message, "正在检查更新…", func(ctx context.Context) error {
		var err error
		release, err = selfupdate.Check(ctx, server)
		return err
	}, func(err error) {
		if err != nil {
			ui.setMessage(err.Error(), ui.colors.danger)
			return
		}
		if release == nil {
			ui.setMessage("当前已是服务器提供的最新版本。", ui.colors.success)
			return
		}
		accepted, err := ui.confirmUpdate(release.Version)
		switch {
		case err != nil:
			ui.setMessage("无法打开更新确认窗口。", ui.colors.danger)
		case !accepted:
			ui.setMessage("已取消更新。", ui.colors.accent)
		default:
			ui.installUpdate(release)
		}
	})
}

func (ui *nativeWindow) confirmUpdate(version string) (bool, error) {
	ui.show()
	ui.confirmingAction = true
	defer func() { ui.confirmingAction = false }()
	var dialog *walk.Dialog
	var cancel *walk.PushButton
	err := (d.Dialog{
		AssignTo: &dialog, Title: "KeyDock Connect 更新", FixedSize: true,
		Font: uiFont(10, false), MinSize: d.Size{Width: 408},
		CancelButton: &cancel, DefaultButton: &cancel, Layout: d.VBox{},
		Children: []d.Widget{
			d.Label{Text: "发现新版本 v" + version, Font: uiFont(12, true)},
			d.TextLabel{Text: "更新会关闭当前代理并退出助手。请在 Windows 提示时批准管理员权限，之后将自动安装。",
				NoPrefix: true, MinSize: d.Size{Width: 360}, MaxSize: d.Size{Width: 420}},
			d.Composite{Layout: d.HBox{MarginsZero: true}, Children: []d.Widget{
				d.HSpacer{},
				d.PushButton{AssignTo: &cancel, Text: "取消", OnClicked: func() { dialog.Cancel() }},
				d.PushButton{Text: "在线更新", OnClicked: func() { dialog.Accept() }},
			}},
		},
	}).Create(ui.window)
	if dialog != nil {
		defer func() {
			if !dialog.IsDisposed() {
				dialog.Dispose()
			}
		}()
	}
	if err != nil {
		return false, err
	}
	return dialog.Run() == walk.DlgCmdOK, nil
}

func (ui *nativeWindow) installUpdate(release *selfupdate.Release) {
	owner := windows.Handle(ui.window.Handle())
	ui.runTask(ui.message, "正在下载并校验安装包…", func(ctx context.Context) error {
		path, err := selfupdate.Download(ctx, release)
		if err != nil {
			return err
		}
		launched := false
		defer func() {
			if !launched {
				os.RemoveAll(filepath.Dir(path))
			}
		}()

		installer, err := lockWindowsInstaller(path)
		if err != nil {
			return errors.New("无法锁定已下载的安装包，请重试。")
		}
		defer installer.Close()
		if !release.Verify(installer) {
			return errors.New("安装包校验失败，请重新下载。")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		ui.app.Synchronize(func() {
			if !ui.quitting {
				ui.setMessage("正在关闭代理并启动安装程序…", ui.colors.accent)
			}
		})
		if err := ui.controller.Close(); err != nil {
			return errors.New("更新前无法安全关闭代理：" + err.Error())
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := launchWindowsSetup(owner, path); err != nil {
			if errors.Is(err, windows.ERROR_CANCELLED) {
				return errors.New("已取消安装。可再次检查更新。")
			}
			return errors.New("无法启动安装程序，请重试或打开软件下载页。")
		}
		launched = true
		return nil
	}, func(err error) {
		if err != nil {
			ui.setMessage(err.Error(), ui.colors.danger)
			return
		}
		ui.exitAfterCleanup()
	})
}

type lockedWindowsInstaller struct {
	*os.File
	directory *os.File
}

func (installer *lockedWindowsInstaller) Close() error {
	return errors.Join(installer.File.Close(), installer.directory.Close())
}

// Keep the staging directory and the installer in place through Setup launch.
// Only the installer handle is used for verification.
func lockWindowsInstaller(path string) (*lockedWindowsInstaller, error) {
	dir, err := openLockedUpdatePath(filepath.Dir(path), true)
	if err != nil {
		return nil, err
	}
	installer, err := openLockedUpdatePath(path, false)
	if err != nil {
		dir.Close()
		return nil, err
	}
	return &lockedWindowsInstaller{File: installer, directory: dir}, nil
}

func openLockedUpdatePath(path string, directory bool) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	isDir := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	if isDir != directory || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(handle)
		return nil, errors.New("installer path is not a regular file or directory")
	}
	return os.NewFile(uintptr(handle), path), nil
}

type setupExecuteInfo struct {
	size                        uint32
	mask                        uint32
	owner                       windows.Handle
	verb, file, parameters, dir *uint16
	show                        int32
	instance                    windows.Handle
	itemList                    unsafe.Pointer
	class                       *uint16
	classKey                    windows.Handle
	hotKey                      uint32
	iconOrMonitor, process      windows.Handle
}

var shellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

func launchWindowsSetup(owner windows.Handle, path string) error {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr := win.CoInitializeEx(nil, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE)
	if hr != win.S_OK && hr != win.S_FALSE {
		return errors.New("could not initialize the Windows installer launcher")
	}
	defer win.CoUninitialize()

	const seeMaskNoAsync = 0x00000100
	const seeMaskFlagNoUI = 0x00000400
	info := setupExecuteInfo{
		mask: seeMaskNoAsync | seeMaskFlagNoUI, owner: owner,
		verb: windows.StringToUTF16Ptr("runas"), file: windows.StringToUTF16Ptr(path), dir: windows.StringToUTF16Ptr(system),
		parameters: windows.StringToUTF16Ptr("/SP- /SILENT /NORESTART /LANG=zh /KEYDOCKUPDATEPID=" + strconv.Itoa(os.Getpid())),
		show:       win.SW_SHOWNORMAL,
	}
	info.size = uint32(unsafe.Sizeof(info))
	if ok, _, err := shellExecuteEx.Call(uintptr(unsafe.Pointer(&info))); ok == 0 {
		return err
	}
	return nil
}
