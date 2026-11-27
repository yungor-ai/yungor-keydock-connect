package proxy

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ipHelper            = windows.NewLazySystemDLL("iphlpapi.dll")
	bestInterface       = ipHelper.NewProc("GetBestInterfaceEx")
	deleteRoute         = ipHelper.NewProc("DeleteIpForwardEntry2")
	consoleAPI          = windows.NewLazySystemDLL("kernel32.dll")
	attachConsole       = consoleAPI.NewProc("AttachConsole")
	freeConsole         = consoleAPI.NewProc("FreeConsole")
	consoleProcesses    = consoleAPI.NewProc("GetConsoleProcessList")
	setConsoleHandler   = consoleAPI.NewProc("SetConsoleCtrlHandler")
	networkAdapterClass = windows.GUID{Data1: 0x4d36e972, Data2: 0xe325, Data3: 0x11ce,
		Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18}}
)

func (m *Manager) CheckMode(mode Mode) error {
	switch mode {
	case ModeSystem:
		return nil
	case ModeTUN:
		if err := checkProtectedTUNInstall(m.corePath); err != nil {
			return err
		}
		if !windows.GetCurrentProcessToken().IsElevated() {
			return errors.New("启用 TUN 需要管理员权限。请从托盘退出助手，在开始菜单右键 KeyDock Connect，选择「更多」→「以管理员身份运行」，并使用当前 Windows 账号")
		}
		return nil
	default:
		return errors.New("无效的代理模式")
	}
}

func reserveTUN() (windows.Handle, error) {
	name := windows.StringToUTF16Ptr(`Global\Yungor.KeyDockConnect.TUN`)
	lock, err := windows.CreateMutex(nil, false, name)
	if err == nil {
		return lock, nil
	}
	if lock != 0 {
		_ = windows.CloseHandle(lock)
	}
	return 0, errors.New("另一个 KeyDock Connect 实例正在使用 TUN。请先关闭该实例的 TUN")
}

func checkTUNStart(ctx context.Context) error {
	current, err := currentSettings()
	if err != nil {
		return err
	}
	const manualOrScript = 2 | 4
	if current.Flags&manualOrScript != 0 {
		return errors.New("Windows 已开启手动代理或代理设置脚本。请关闭该设置后再启用 TUN")
	}
	return waitForTUNIdle(ctx)
}

// sing-tun derives the Wintun adapter's requested GUID from its device name.
// Preflight checks both the alias and GUID before reusing an idle device.
// The GUID also identifies our interface after startup.
func tunAdapterGUID() windows.GUID {
	sum := md5.Sum([]byte("wintun" + tunDeviceName))
	return windows.GUID{
		Data1: binary.LittleEndian.Uint32(sum[:4]),
		Data2: binary.LittleEndian.Uint16(sum[4:6]),
		Data3: binary.LittleEndian.Uint16(sum[6:8]),
		Data4: [8]byte(sum[8:]),
	}
}

func findTUNAdapter() (luid uint64, index uint32, err error) {
	return lookupTUNAdapter(true)
}

func lookupTUNAdapter(checkAlias bool) (luid uint64, index uint32, err error) {
	row, err := tunInterface(checkAlias)
	if err != nil || row == nil {
		return 0, 0, err
	}
	return row.InterfaceLuid, row.InterfaceIndex, nil
}

func tunInterface(checkAlias bool) (*windows.MibIfRow2, error) {
	var table *windows.MibIfTable2
	if err := windows.GetIfTable2Ex(windows.MibIfTableNormalWithoutStatistics, &table); err != nil {
		return nil, err
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	guid := tunAdapterGUID()
	rows := unsafe.Slice(&table.Table[0], table.NumEntries)
	for index := range rows {
		row := &rows[index]
		if row.InterfaceGuid == guid || checkAlias && strings.EqualFold(windows.UTF16ToString(row.Alias[:]), tunDeviceName) {
			result := *row
			return &result, nil
		}
	}
	return nil, nil
}

func tunRoutes(luid uint64) ([]windows.MibIpForwardRow2, error) {
	if luid == 0 {
		return nil, nil
	}
	var table *windows.MibIpForwardTable2
	if err := windows.GetIpForwardTable2(windows.AF_UNSPEC, &table); err != nil {
		return nil, err
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	var defaults []windows.MibIpForwardRow2
	for _, row := range table.Rows() {
		if row.InterfaceLuid == luid && row.DestinationPrefix.PrefixLength == 0 {
			defaults = append(defaults, row)
		}
	}
	return defaults, nil
}

func getBestInterface() (uint32, error) {
	// The Windows route lookup is local. The documentation-only destination
	// tests default selection without making a network request.
	address := windows.RawSockaddrInet4{Family: windows.AF_INET, Addr: [4]byte{203, 0, 113, 1}}
	return selectInterface(unsafe.Pointer(&address))
}

func getBestIPv6Interface() (uint32, error) {
	address := windows.RawSockaddrInet6{Family: windows.AF_INET6, Addr: [16]byte{0x20, 0x01, 0x0d, 0xb8, 15: 1}}
	return selectInterface(unsafe.Pointer(&address))
}

func selectInterface(address unsafe.Pointer) (uint32, error) {
	var index uint32
	rc, _, _ := bestInterface.Call(uintptr(address), uintptr(unsafe.Pointer(&index)))
	if rc != 0 {
		return 0, windows.Errno(rc)
	}
	return index, nil
}

type tunWaitStep struct{ stage, message string }

var (
	tunWaitCore         = tunWaitStep{"proxy.tun_core_ready", "代理核心尚未完成 TUN 初始化"}
	tunWaitAdapter      = tunWaitStep{"proxy.tun_adapter", "Windows 尚未准备好 KeyDockTun 虚拟网卡"}
	tunWaitIPv4Address  = tunWaitStep{"proxy.tun_ipv4_address", "KeyDockTun 尚未获得所需的 IPv4 地址 198.18.0.1/30"}
	tunWaitDefaultRoute = tunWaitStep{"proxy.tun_default_route", "Windows 尚未为 KeyDockTun 建立 IPv4 默认路由"}
	tunWaitMTU          = tunWaitStep{"proxy.tun_mtu", "KeyDockTun 的 IPv4 MTU 尚未设置为 1500"}
	tunWaitIPv4Route    = tunWaitStep{"proxy.tun_ipv4_best", "Windows 尚未将 IPv4 流量路由到 KeyDockTun"}
	tunWaitIPv6Route    = tunWaitStep{"proxy.tun_ipv6_best", "Windows 已为 KeyDockTun 建立 IPv6 路由，但尚未选用该网卡"}
)

func (m *Manager) awaitTUN(ctx context.Context, p *child) (resultErr error) {
	started := time.Now()
	defer func() { m.logStage("proxy.tun_ready", started, resultErr) }()
	// The mixed listener starts first. Wintun 0.14.1 can wait 15s for its
	// device interface and Mihomo's Windows tunNew allows three attempts.
	deadline := time.NewTimer(60 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	pending := &tunWaitAdapter
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-p.tunStartupFailure:
			return err
		case <-p.done:
			return p.tunStartupError(errors.New("代理核心在 TUN 网卡就绪前退出"))
		case <-deadline.C:
			if err := p.tunStartupError(nil); err != nil {
				return err
			}
			if lastErr == nil {
				lastErr = context.DeadlineExceeded
			}
			m.logStage(pending.stage, started, lastErr)
			return errors.New(pending.message + "。请在窗口底部导出诊断日志，交给管理员查看。")
		case <-ticker.C:
			pending, lastErr = p.pendingTUNStep()
			if pending == nil {
				// Addresses and routes are visible before strict-route finishes
				// installing its filters. Also require the core's TUN listen event.
				select {
				case <-p.tunCoreReady:
					return p.tunStartupError(nil)
				default:
					pending = &tunWaitCore
				}
			}
		}
	}
}

func (p *child) pendingTUNStep() (*tunWaitStep, error) {
	row, err := tunInterface(false)
	if err != nil || row == nil || row.InterfaceLuid == 0 || row.InterfaceIndex == 0 ||
		!strings.EqualFold(windows.UTF16ToString(row.Alias[:]), tunDeviceName) {
		return &tunWaitAdapter, err
	}
	// GetIfTable2Ex can see Wintun before Go's GetAdaptersAddresses snapshot.
	// Use the identified adapter's LUID throughout instead of matching it again.
	luid := row.InterfaceLuid
	p.tunLUID = luid
	address := windows.MibUnicastIpAddressRow{InterfaceLuid: luid}
	*(*windows.RawSockaddrInet4)(unsafe.Pointer(&address.Address)) = windows.RawSockaddrInet4{
		Family: windows.AF_INET, Addr: [4]byte{198, 18, 0, 1},
	}
	if err := windows.GetUnicastIpAddressEntry(&address); err != nil || address.OnLinkPrefixLength != 30 {
		return &tunWaitIPv4Address, err
	}
	routes, err := tunRoutes(luid)
	if err != nil {
		return &tunWaitDefaultRoute, err
	}
	var ipv4Route, ipv6Route bool
	for _, route := range routes {
		ipv4Route = ipv4Route || route.DestinationPrefix.Prefix.Family == windows.AF_INET
		ipv6Route = ipv6Route || route.DestinationPrefix.Prefix.Family == windows.AF_INET6
	}
	if !ipv4Route {
		return &tunWaitDefaultRoute, nil
	}
	// Wintun publishes its routes before applying the adapter's MTU.
	ipv4Interface := windows.MibIpInterfaceRow{Family: windows.AF_INET, InterfaceLuid: luid}
	if err := windows.GetIpInterfaceEntry(&ipv4Interface); err != nil || ipv4Interface.NlMtu != windowsTUNMTU {
		return &tunWaitMTU, err
	}
	if selected, err := getBestInterface(); err != nil || selected != row.InterfaceIndex {
		return &tunWaitIPv4Route, err
	}
	// A reused adapter can retain an IPv6 address from an older version.
	// Only verify IPv6 route selection when this session installed a route.
	if ipv6Route {
		if selected, err := getBestIPv6Interface(); err != nil || selected != row.InterfaceIndex {
			return &tunWaitIPv6Route, err
		}
	}
	return nil, nil
}

// Mihomo handles Go's Interrupt signal by closing the TUN listener and Wintun
// adapter. Only send CTRL+C after confirming the console is private to us.
func (p *child) requestTUNShutdown() {
	if ok, _, _ := attachConsole.Call(uintptr(p.cmd.Process.Pid)); ok == 0 {
		return
	}
	defer freeConsole.Call()
	var processes [2]uint32
	n, _, _ := consoleProcesses.Call(uintptr(unsafe.Pointer(&processes[0])), uintptr(len(processes)))
	pid, self := uint32(p.cmd.Process.Pid), uint32(os.Getpid())
	if n != 2 || processes != [2]uint32{pid, self} && processes != [2]uint32{self, pid} {
		return
	}
	if ok, _, _ := setConsoleHandler.Call(0, 1); ok == 0 {
		return
	}
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_C_EVENT, 0); err != nil {
		return
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
	}
}

func (p *child) clearTUNRoutes() error {
	if p.tunLUID == 0 {
		luid, _, err := lookupTUNAdapter(false)
		if err != nil {
			return err
		}
		p.tunLUID = luid
	}
	routes, err := tunRoutes(p.tunLUID)
	if err != nil {
		return err
	}
	for index := range routes {
		rc, _, _ := deleteRoute.Call(uintptr(unsafe.Pointer(&routes[index])))
		if rc != 0 && windows.Errno(rc) != windows.ERROR_NOT_FOUND {
			return windows.Errno(rc)
		}
	}
	routes, err = tunRoutes(p.tunLUID)
	if err != nil {
		return err
	}
	if len(routes) != 0 {
		return errors.New("KeyDock TUN 路由未恢复，请联系管理员检查网络适配器")
	}
	return nil
}

// With the machine-wide reservation held, allow sing-tun to reopen its own
// idle adapter. Windows can keep the network-interface row after it unplugs
// a software device. No routed or unrelated adapter may be reused.
func waitForTUNIdle(ctx context.Context) error {
	deadline := time.Now().Add(3 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		row, err := tunInterface(true)
		if err != nil || row == nil {
			return err
		}
		if row.InterfaceGuid != tunAdapterGUID() || !strings.EqualFold(windows.UTF16ToString(row.Alias[:]), tunDeviceName) {
			return errors.New("Windows 已有冲突的 KeyDockTun 适配器。请联系管理员检查该网卡后再启用 TUN")
		}
		routes, err := tunRoutes(row.InterfaceLuid)
		if err != nil {
			return err
		}
		down := row.OperStatus == windows.IfOperStatusDown || row.OperStatus == windows.IfOperStatusNotPresent ||
			row.OperStatus == windows.IfOperStatusLowerLayerDown
		if down && len(routes) == 0 {
			return verifyTUNDriver()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("KeyDockTun 网卡未进入空闲状态（状态 %d，默认路由 %d）。请重启 Windows 后再启用 TUN", row.OperStatus, len(routes))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func verifyTUNDriver() error {
	const enumerator = `SWD\Wintun`
	devices, err := windows.SetupDiGetClassDevsEx(&networkAdapterClass, enumerator, 0, windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return err
	}
	defer devices.Close()
	wantedID := enumerator + `\` + tunAdapterGUID().String()
	for index := 0; ; index++ {
		info, err := devices.EnumDeviceInfo(index)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			// A cached IP Helper row without a present PnP device may be recreated.
			return nil
		}
		if err != nil {
			return err
		}
		id, err := devices.DeviceInstanceID(info)
		if err != nil {
			return err
		}
		if !strings.EqualFold(id, wantedID) {
			continue
		}
		value, err := devices.DeviceRegistryProperty(info, windows.SPDRP_SERVICE)
		if err != nil {
			return err
		}
		service, ok := value.(string)
		if !ok || !strings.EqualFold(service, "Wintun") {
			return errors.New("KeyDockTun 的驱动与内置 Wintun 不一致。请联系管理员检查网卡")
		}
		return nil
	}
}
