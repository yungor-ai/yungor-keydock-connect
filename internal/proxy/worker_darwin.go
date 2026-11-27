//go:build darwin && arm64

package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// Developer ID builds replace this digest after signing the verified upstream core.
var coreDigest = "a55a9073f9b7b1fa596a82c034d2dae3407ec34732102b6e42c5513ce3033f34"

const runtimePrefix = "proxy-runtime-"
const maxAssetSize = 64 << 20

var workerFailures = [...]string{
	"内置代理运行失败",
	"无法创建私有代理运行目录",
	"内置代理核心校验失败，请重新安装",
	"内置离线分流数据校验失败，请重新安装",
	"macOS 无法启动内置代理核心",
}

// RunWorker is the private subprocess entry point. It starts only the
// pinned native core with a validated, loopback-only KeyDock profile.
// A parent-owned pipe makes an unexpected exit equivalent to stopping it.
func RunWorker(args []string) (bool, error) {
	if (len(args) == 1 || len(args) == 2) && args[0] == "--tun-daemon" {
		return true, runDarwinTUNService()
	}
	if len(args) == 0 || args[0] != "--proxy-worker" {
		return false, nil
	}
	if len(args) != 4 || !filepath.IsAbs(args[1]) {
		return true, errors.New("无效的本机代理进程参数")
	}
	port, err := strconv.Atoi(args[2])
	if err != nil || port < 1 || port > 65535 {
		return true, errors.New("无效的本机代理端口")
	}
	purpose := args[3]
	if purpose != "run" && purpose != "test" && purpose != "validate" {
		return true, errors.New("无效的本机代理进程类型")
	}
	lifeline := os.NewFile(3, "parent-lifetime")
	if lifeline == nil {
		return true, errors.New("无法核对本机代理进程的父进程")
	}
	defer lifeline.Close()
	info, err := lifeline.Stat()
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		return true, errors.New("无法核对本机代理进程的父进程")
	}
	unix.CloseOnExec(int(lifeline.Fd()))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, lifeline)
		cancel()
	}()

	return true, runDarwinCore(ctx, args[1], port, purpose)
}

func runDarwinCore(ctx context.Context, bundled string, port int, purpose string) error {
	body, err := io.ReadAll(io.LimitReader(os.Stdin, profileLimit+1))
	if err != nil {
		return errors.New("无法读取本机代理配置")
	}
	cfg, err := buildRoutingProfile(body, port, purpose == "test", ModeLocal)
	if err != nil {
		return err
	}
	fail := func(code int) error {
		_, _ = fmt.Fprintln(os.Stdout, -code)
		return errors.New(workerFailures[code])
	}
	dir, err := os.MkdirTemp("", runtimePrefix)
	if err != nil {
		return fail(1)
	}
	defer os.RemoveAll(dir)
	core := filepath.Join(dir, "mihomo")
	if err = copyDarwinAsset(bundled, core, coreDigest, 0500); err != nil {
		return fail(2)
	}
	for name, hash := range geoFiles {
		if err = copyDarwinAsset(filepath.Join(filepath.Dir(bundled), name), filepath.Join(dir, name), hash, 0600); err != nil {
			return fail(3)
		}
	}
	args := []string{"-d", dir, "-f", "-"}
	if purpose == "validate" {
		args = append(args, "-t")
	}
	cmd := exec.CommandContext(ctx, core, args...)
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + dir, "TMPDIR=" + dir}
	cmd.Stdin = bytes.NewReader(cfg)
	if err = cmd.Start(); err != nil {
		return fail(4)
	}
	if _, err = fmt.Fprintln(os.Stdout, cmd.Process.Pid, filepath.Base(dir)); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.New("无法确认本机代理进程启动")
	}
	if err = cmd.Wait(); err != nil && ctx.Err() == nil {
		return errors.New(workerFailures[0])
	}
	return nil
}

func copyDarwinAsset(source, dest, hash string, permission os.FileMode) error {
	fd, err := unix.Open(source, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	in := os.NewFile(uintptr(fd), source)
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxAssetSize {
		return errors.New("无效的内置代理组件")
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, permission)
	if err != nil {
		return err
	}
	err = verifyDigest(io.LimitReader(in, maxAssetSize+1), out, hash)
	return errors.Join(err, out.Close())
}
