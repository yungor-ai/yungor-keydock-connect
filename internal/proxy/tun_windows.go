package proxy

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ipHelper           = windows.NewLazySystemDLL("iphlpapi.dll")
	bestInterface      = ipHelper.NewProc("GetBestInterfaceEx")
	deleteRoute        = ipHelper.NewProc("DeleteIpForwardEntry2")
	consoleAPI         = windows.NewLazySystemDLL("kernel32.dll")
	attachConsole      = consoleAPI.NewProc("AttachConsole")
	freeConsole        = consoleAPI.NewProc("FreeConsole")
	consoleProcesses   = consoleAPI.NewProc("GetConsoleProcessList")
	setConsoleHandler  = consoleAPI.NewProc("SetConsoleCtrlHandler")
	errTUNStillPresent = errors.New("KeyDockTun 适配器尚未移除")
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
			return errors.New("TUN 需要管理员权限。请从系统托盘退出接入助手，再以同一 Windows 用户右键选择「以管理员身份运行」")
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

func checkTUNStart() error {
	current, err := currentSettings()
	if err != nil {
		return err
	}
	const manualOrScript = 2 | 4
	if current.Flags&manualOrScript != 0 {
		return errors.New("Windows 已开启手动代理或代理设置脚本。请关闭该设置后再启用 TUN")
	}
	// The Windows PnP removal after process or handle closure is asynchronous.
	// We hold the cross-session reservation here, so a new KeyDock worker
	// cannot claim the adapter while Windows finishes the previous removal.
	if err := waitForTUNRemoval(); err != nil {
		if errors.Is(err, errTUNStillPresent) {
			return errors.New("检测到 KeyDockTun 虚拟网卡仍存在。请重启 Windows 后再启用 TUN。若仍出现此提示，请联系管理员检查该虚拟网卡")
		}
		return err
	}
	luid, _, err := findTUNAdapter()
	if err != nil {
		return err
	}
	if luid != 0 {
		return errors.New("Windows 已有同名的 KeyDockTun 适配器。请联系管理员检查该网卡名称后再启用 TUN")
	}
	return nil
}

// sing-tun derives the Wintun adapter's requested GUID from its device name.
// Check both the alias and GUID during preflight so an existing device is
// never reused. A GUID match also identifies our new device after startup.
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
	var table *windows.MibIfTable2
	if err := windows.GetIfTable2Ex(windows.MibIfTableNormalWithoutStatistics, &table); err != nil {
		return 0, 0, err
	}
	defer windows.FreeMibTable(unsafe.Pointer(table))
	guid := tunAdapterGUID()
	rows := unsafe.Slice(&table.Table[0], table.NumEntries)
	for index := range rows {
		row := &rows[index]
		if row.InterfaceGuid == guid || checkAlias && strings.EqualFold(windows.UTF16ToString(row.Alias[:]), tunDeviceName) {
			// The LUID identifies this route table even if the alias changes.
			return row.InterfaceLuid, row.InterfaceIndex, nil
		}
	}
	return 0, 0, nil
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

func (m *Manager) awaitTUN(ctx context.Context, p *child) (resultErr error) {
	started := time.Now()
	defer func() { m.logStage("proxy.tun_ready", started, resultErr) }()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return errors.New("代理核心在 TUN 网卡就绪前退出")
		case <-deadline.C:
			return errors.Join(errors.New("Windows TUN 网卡或接管路由未就绪，请检查权限及其他代理或 VPN"), lastErr)
		case <-ticker.C:
			var ready bool
			ready, lastErr = p.tunReady()
			if ready {
				return nil
			}
		}
	}
}

func (p *child) tunReady() (bool, error) {
	luid, index, err := lookupTUNAdapter(false)
	if err != nil || luid == 0 {
		return false, err
	}
	nic, err := net.InterfaceByIndex(int(index))
	if err != nil || !strings.EqualFold(nic.Name, tunDeviceName) {
		return false, err
	}
	addresses, err := nic.Addrs()
	if err != nil {
		return false, err
	}
	var ipv4Assigned, ipv6Assigned bool
	for _, address := range addresses {
		ipv4Assigned = ipv4Assigned || address.String() == "198.18.0.1/30"
		if ipnet, ok := address.(*net.IPNet); ok && ipnet.IP.To4() == nil && !ipnet.IP.IsLinkLocalUnicast() {
			ipv6Assigned = true
		}
	}
	p.tunLUID = luid
	routes, err := tunRoutes(luid)
	if err != nil {
		return false, err
	}
	var ipv4Route, ipv6Route bool
	for _, route := range routes {
		ipv4Route = ipv4Route || route.DestinationPrefix.Prefix.Family == windows.AF_INET
		ipv6Route = ipv6Route || route.DestinationPrefix.Prefix.Family == windows.AF_INET6
	}
	if !ipv4Assigned || !ipv4Route || ipv6Assigned && !ipv6Route {
		return false, nil
	}
	selected, err := getBestInterface()
	if err != nil || selected != index {
		return false, err
	}
	if ipv6Route {
		selected, err = getBestIPv6Interface()
		return selected == index, err
	}
	return true, nil
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
	err = waitForTUNRemoval()
	if errors.Is(err, errTUNStillPresent) {
		return errors.New("KeyDock TUN 路由已撤销，虚拟网卡仍存在。请重启 Windows。若仍出现此提示，请联系管理员检查该虚拟网卡")
	}
	return err
}

func waitForTUNRemoval() error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		luid, _, err := lookupTUNAdapter(false)
		if err != nil || luid == 0 {
			return err
		}
		if time.Now().After(deadline) {
			return errTUNStillPresent
		}
		time.Sleep(100 * time.Millisecond)
	}
}
