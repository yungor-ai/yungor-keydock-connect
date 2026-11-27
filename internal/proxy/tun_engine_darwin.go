//go:build darwin && arm64

package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	darwinTUNDevice  = "utun3917"
	darwinTUNIPv4    = "198.18.0.1"
	darwinTUNRuntime = "/private/var/tmp"
)

// These addresses are queried in the kernel's routing table only. Sample both
// halves of IPv4 to check actual route selection.
var darwinTUNProbeAddresses = [...]netip.Addr{
	netip.MustParseAddr("1.0.0.1"),
	netip.MustParseAddr("208.67.222.222"),
}

func buildDarwinTUNProfile(body []byte) ([]byte, error) {
	tun := makeTUNSettings(darwinTUNDevice)
	// sing-tun publishes a process-scoped default resolver in SystemConfiguration.
	// Leave dns-hijack empty so domain-specific VPN resolvers can answer directly.
	tun.DNSMode = "native"
	tun.Inet6Address = &[]string{}
	return buildRoutingProfileWithTUN(body, 0, false, ModeTUN, tun, "")
}

// runDarwinTUN runs inside the launchd helper. The GUI never owns the
// elevated process, its configuration, or its runtime files.
func runDarwinTUN(ctx context.Context, corePath string, body []byte, notify func() error) (result error) {
	if os.Geteuid() != 0 {
		return errors.New("TUN 需要已获授权的 KeyDock 管理员服务")
	}
	startup, cancelStartup := context.WithTimeout(ctx, 12*time.Second)
	defer cancelStartup()
	if err := checkDarwinTUNStart(startup); err != nil {
		return err
	}
	cfg, err := buildDarwinTUNProfile(body)
	if err != nil {
		return err
	}
	runtimeDir, core, err := prepareDarwinTUNRuntime(corePath)
	if err != nil {
		return err
	}
	defer os.RemoveAll(runtimeDir)
	if err := startup.Err(); err != nil {
		return err
	}
	cmd := exec.Command(core, "-d", runtimeDir, "-f", "-")
	cmd.Dir = runtimeDir
	cmd.Env = []string{"HOME=" + runtimeDir, "TMPDIR=" + runtimeDir, "PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	cmd.Stdin = bytes.NewReader(cfg)
	if err := cmd.Start(); err != nil {
		return errors.New("macOS 无法启动内置 TUN 代理核心")
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	defer func() {
		stopErr := stopDarwinTUNCore(cmd.Process, done)
		cleanupErr := waitDarwinTUNCleanup()
		result = errors.Join(result, stopErr, cleanupErr)
	}()
	if err = awaitDarwinTUN(startup, done); err != nil {
		return err
	}
	if err = notify(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return nil
	case <-done:
		return errors.New("KeyDock TUN 代理核心意外退出")
	}
}

func prepareDarwinTUNRuntime(corePath string) (string, string, error) {
	// A normal user cannot replace a root-owned child of this root-owned
	// sticky directory while the helper verifies and executes the core.
	var parent unix.Stat_t
	if err := unix.Lstat(darwinTUNRuntime, &parent); err != nil || parent.Uid != 0 ||
		parent.Mode&unix.S_IFMT != unix.S_IFDIR || parent.Mode&unix.S_ISVTX == 0 {
		return "", "", errors.New("无法创建受保护的 TUN 运行目录")
	}
	dir, err := os.MkdirTemp(darwinTUNRuntime, "keydock-tun-")
	if err != nil {
		return "", "", errors.New("无法创建受保护的 TUN 运行目录")
	}
	core := filepath.Join(dir, "mihomo")
	if err = copyDarwinAsset(corePath, core, coreDigest, 0500); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", errors.New(workerFailures[2])
	}
	for name, hash := range geoFiles {
		if err = copyDarwinAsset(filepath.Join(filepath.Dir(corePath), name), filepath.Join(dir, name), hash, 0600); err != nil {
			_ = os.RemoveAll(dir)
			return "", "", errors.New(workerFailures[3])
		}
	}
	return dir, core, nil
}

func checkDarwinTUNStart(ctx context.Context) error {
	clean, err := darwinTUNAbsent(ctx)
	if err != nil {
		return err
	}
	if !clean {
		return errors.New("KeyDock TUN 网卡已被占用或仍有接管路由，请先关闭使用该网卡的工具")
	}
	for _, address := range darwinTUNProbeAddresses {
		name, err := darwinRouteInterface(ctx, address)
		if err != nil {
			return err
		}
		if strings.HasPrefix(name, "utun") || strings.HasPrefix(name, "tun") || strings.HasPrefix(name, "ipsec") || strings.HasPrefix(name, "ppp") {
			return fmt.Errorf("IPv4 流量已由 VPN 接口 %s 接管。请先在原 VPN 或代理工具中断开连接，再启用 KeyDock TUN", name)
		}
		if name == "" {
			return errors.New("macOS 当前没有可用的 IPv4 互联网路由，请先连接可访问 IPv4 的网络")
		}
	}
	return nil
}

func darwinRouteInterface(ctx context.Context, address netip.Addr) (string, error) {
	family := "-inet"
	if address.Is6() {
		family = "-inet6"
	}
	cmd := exec.CommandContext(ctx, "/sbin/route", "-n", "get", family, address.String())
	cmd.Env = []string{"LC_ALL=C"}
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return parseDarwinRouteInterface(output, err)
}

func parseDarwinRouteInterface(output []byte, err error) (string, error) {
	// BSD route can write this to stderr and still exit successfully.
	if bytes.Contains(output, []byte("not in table")) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("无法检查 macOS 互联网路由")
	}
	for _, line := range strings.Split(string(output), "\n") {
		if name, ok := strings.CutPrefix(strings.TrimSpace(line), "interface:"); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name), nil
		}
	}
	return "", errors.New("无法识别 macOS 互联网路由接口")
}

func awaitDarwinTUN(ctx context.Context, done <-chan struct{}) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return errors.New("KeyDock TUN 网卡或接管路由未就绪，请检查网络和其他代理或 VPN")
		case <-done:
			return errors.New("代理核心在 KeyDock TUN 网卡就绪前退出")
		case <-ticker.C:
			ready, err := darwinTUNReady(ctx)
			if err != nil {
				return err
			}
			if ready {
				return nil
			}
		}
	}
}

func darwinTUNReady(ctx context.Context) (bool, error) {
	iface, err := net.InterfaceByName(darwinTUNDevice)
	if err != nil || iface.Flags&net.FlagUp == 0 {
		return false, nil
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return false, nil
	}
	ipv4Assigned := false
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err == nil && prefix.Addr().String() == darwinTUNIPv4 {
			ipv4Assigned = true
			break
		}
	}
	if !ipv4Assigned {
		return false, nil
	}
	for _, address := range darwinTUNProbeAddresses {
		name, err := darwinRouteInterface(ctx, address)
		if name != darwinTUNDevice || err != nil {
			return false, err
		}
	}
	return true, nil
}

func stopDarwinTUNCore(process *os.Process, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	default:
	}
	if err := process.Signal(unix.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return errors.New("无法通知 KeyDock TUN 代理核心退出")
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return errors.New("无法结束 KeyDock TUN 代理核心")
	}
	timer.Reset(time.Second)
	select {
	case <-done:
		return nil
	case <-timer.C:
		return errors.New("KeyDock TUN 代理核心未能及时退出")
	}
}

func waitDarwinTUNCleanup() error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		clean, err := darwinTUNAbsent(ctx)
		if clean && err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.Join(errors.New("KeyDock TUN 路由未恢复，请让管理员检查系统网络"), err)
		case <-ticker.C:
		}
	}
}

func darwinTUNAbsent(ctx context.Context) (bool, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return false, errors.New("无法检查 macOS 网络接口")
	}
	for _, iface := range interfaces {
		if iface.Name == darwinTUNDevice {
			return false, nil
		}
	}
	output, err := exec.CommandContext(ctx, "/usr/sbin/netstat", "-rn").Output()
	if err != nil {
		return false, errors.New("无法检查 macOS 路由表")
	}
	return !darwinRouteTableContains(string(output), darwinTUNDevice), nil
}

func darwinRouteTableContains(output, name string) bool {
	for _, line := range strings.Split(output, "\n") {
		if slices.Contains(strings.Fields(line), name) {
			return true
		}
	}
	return false
}
