package selfupdate

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>

static Boolean kdSelfUpdateTeam(char *team, CFIndex length) {
    SecCodeRef process = NULL;
    SecStaticCodeRef code = NULL;
    CFDictionaryRef signing = NULL;
    Boolean found = false;
    if (SecCodeCopySelf(kSecCSDefaultFlags, &process) == errSecSuccess &&
        SecCodeCopyStaticCode(process, kSecCSDefaultFlags, &code) == errSecSuccess &&
        SecCodeCopySigningInformation(code, kSecCSSigningInformation, &signing) == errSecSuccess) {
        CFTypeRef value = CFDictionaryGetValue(signing, kSecCodeInfoTeamIdentifier);
        if (value && CFGetTypeID(value) == CFStringGetTypeID()) {
            found = CFStringGetCString((CFStringRef)value, team, length, kCFStringEncodingASCII);
        }
    }
    if (signing) CFRelease(signing);
    if (code) CFRelease(code);
    if (process) CFRelease(process);
    return found;
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const macBundle = "KeyDock Connect.app"

type MacInstall struct {
	destination    string
	staging        string
	original       os.FileInfo
	version        string
	checkSignature func(context.Context, string) error
}

// PrepareMacOS copies a verified disk image's app to the destination volume
// before the current app shuts down and releases its proxy and network state.
func PrepareMacOS(ctx context.Context, image, version string) (*MacInstall, error) {
	destination, err := macInstallDestination()
	if err != nil {
		return nil, err
	}
	team, err := runningMacTeam()
	if err != nil {
		return nil, err
	}
	return prepareMacOS(ctx, image, version, destination, func(ctx context.Context, bundle string) error {
		return verifyDeveloperID(ctx, bundle, team)
	})
}

// CheckMacOSInstall catches locations that cannot be updated before download.
func CheckMacOSInstall() error {
	destination, err := macInstallDestination()
	if err != nil {
		return err
	}
	if err := unix.Access(filepath.Dir(destination), unix.W_OK|unix.X_OK); err != nil {
		return errors.New("当前账号无法写入应用所在目录，请在有安装权限的账号中更新")
	}
	_, err = runningMacTeam()
	return err
}

func macInstallDestination() (string, error) {
	binary, err := os.Executable()
	if err != nil {
		return "", errors.New("无法定位正在运行的接入助手")
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return "", errors.New("无法定位正在运行的接入助手")
	}
	macOS := filepath.Dir(binary)
	contents := filepath.Dir(macOS)
	destination := filepath.Dir(contents)
	if filepath.Base(binary) != "KeyDockConnect" || filepath.Base(macOS) != "MacOS" || filepath.Base(contents) != "Contents" ||
		!strings.HasSuffix(destination, ".app") || strings.Contains(destination, "/AppTranslocation/") {
		return "", errors.New("请先将 KeyDock Connect 安装到「应用程序」，再打开助手更新")
	}
	return destination, nil
}

func runningMacTeam() (string, error) {
	const invalidSignature = "请从正式签名的 KeyDock Connect 安装包安装后再检查更新"
	var buffer [11]C.char
	if C.kdSelfUpdateTeam(&buffer[0], C.CFIndex(len(buffer))) == 0 {
		return "", errors.New(invalidSignature)
	}
	team := C.GoString(&buffer[0])
	if len(team) != 10 || strings.IndexFunc(team, func(r rune) bool {
		return (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	}) >= 0 {
		return "", errors.New(invalidSignature)
	}
	return team, nil
}

func verifyDeveloperID(ctx context.Context, bundle, team string) error {
	// Match the publisher and Developer ID signing chain used for the TUN service.
	requirement := fmt.Sprintf(`identifier "ai.yungor.keydock-connect" and anchor apple generic and certificate leaf[subject.OU] = "%s" and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists`, team)
	if err := exec.CommandContext(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", "-R="+requirement, bundle).Run(); err != nil {
		return errors.New("macOS 应用签名与当前安装的发行者不一致")
	}
	return nil
}

func prepareMacOS(ctx context.Context, image, version, destination string, checkSignature func(context.Context, string) error) (*MacInstall, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	original, err := os.Lstat(destination)
	if err != nil || !original.IsDir() {
		return nil, errors.New("无法定位已安装的 KeyDock Connect")
	}
	staging, err := os.MkdirTemp(filepath.Dir(destination), ".keydock-update-")
	if err != nil {
		return nil, errors.New("当前账号无法写入应用所在目录，请在有安装权限的账号中更新")
	}
	prepared := &MacInstall{destination: destination, staging: staging, original: original, version: version, checkSignature: checkSignature}
	keep := false
	defer func() {
		if !keep {
			prepared.Discard()
		}
	}()
	mount, err := os.MkdirTemp("", "keydock-mount-")
	if err != nil {
		return nil, errors.New("无法创建更新挂载目录")
	}
	defer os.Remove(mount)
	if err := exec.CommandContext(ctx, "/usr/bin/hdiutil", "attach", image, "-mountpoint", mount, "-readonly", "-nobrowse", "-noautoopen").Run(); err != nil {
		detachImage(mount)
		return nil, errors.New("无法打开下载的 macOS 安装包")
	}
	source := filepath.Join(mount, macBundle)
	target := filepath.Join(staging, macBundle)
	copyErr := exec.CommandContext(ctx, "/usr/bin/ditto", source, target).Run()
	detachErr := detachImage(mount)
	if copyErr != nil || detachErr != nil {
		return nil, errors.New("无法准备 macOS 更新，请检查可用磁盘空间后重试")
	}
	if err := prepared.verifyBundle(ctx, target); err != nil {
		return nil, err
	}
	keep = true
	return prepared, nil
}

func (update *MacInstall) verifyBundle(ctx context.Context, bundle string) error {
	plist := filepath.Join(bundle, "Contents", "Info.plist")
	bundleVersion, err := exec.CommandContext(ctx, "/usr/bin/plutil", "-extract", "CFBundleShortVersionString", "raw", "-o", "-", plist).Output()
	if err != nil || strings.TrimSpace(string(bundleVersion)) != update.version {
		return errors.New("macOS 安装包中的版本与官方发布不符")
	}
	return update.checkSignature(ctx, bundle)
}

func detachImage(mount string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/hdiutil", "detach", mount).Run()
}

// Discard removes staging files. After a successful swap these are the old app.
func (update *MacInstall) Discard() {
	if update.staging != "" {
		os.RemoveAll(update.staging)
	}
}

// Install swaps both bundles atomically and queues launch after this app exits.
func (update *MacInstall) Install() error {
	current, err := os.Lstat(update.destination)
	if err != nil || !os.SameFile(current, update.original) {
		return errors.New("已安装的应用位置发生变化，请重新检查更新")
	}
	staged := filepath.Join(update.staging, macBundle)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := update.verifyBundle(ctx, staged); err != nil {
		return err
	}
	if err := swapMacBundles(staged, update.destination); err != nil {
		return errors.New("无法替换 KeyDock Connect，请检查应用目录权限后重试")
	}
	if err := relaunchMacApp(update.destination); err != nil {
		if swapMacBundles(staged, update.destination) != nil {
			// Keep the old bundle available if the rollback could not swap it back.
			update.staging = ""
			return errors.New("更新已安装，无法自动重启，请退出后从「应用程序」打开新版")
		}
		return errors.New("无法自动重启 KeyDock Connect，本次更新已还原")
	}
	return nil
}

func swapMacBundles(first, second string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, first, unix.AT_FDCWD, second, unix.RENAME_SWAP)
}

func relaunchMacApp(app string) error {
	const waitForQuit = `
attempt=0
while kill -0 "$1" 2>/dev/null; do
    attempt=$((attempt + 1))
    [ "$attempt" -lt 300 ] || exit 1
    /bin/sleep 0.2
done
exec /usr/bin/open -n -a "$2"
`
	cmd := exec.Command("/bin/sh", "-c", waitForQuit, "keydock-relaunch", strconv.Itoa(os.Getpid()), app)
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}
