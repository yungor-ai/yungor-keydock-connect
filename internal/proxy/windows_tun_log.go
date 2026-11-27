package proxy

import "bytes"

type tunStartupError struct{ class, message string }

func (e tunStartupError) Error() string           { return e.message }
func (e tunStartupError) DiagnosticClass() string { return e.class }

// The pinned Mihomo writes logrus text records to stdout. Inspect only the
// beginning of each record and publish a fixed classification. Never pass
// process output to the UI or diagnostic files: it can contain device secrets.
type tunCoreLog struct {
	ready    chan struct{}
	failure  chan error
	prefix   [256]byte
	used     int
	reported bool
}

// os/exec serializes writes when stdout and stderr share this writer.
func (l *tunCoreLog) Write(data []byte) (int, error) {
	n := len(data)
	for len(data) > 0 && !l.reported {
		part, rest, newline := bytes.Cut(data, []byte{'\n'})
		l.used += copy(l.prefix[l.used:], part)
		if !newline {
			break
		}
		timestamp, fields, found := bytes.Cut(l.prefix[:l.used], []byte{' '})
		if found && bytes.HasPrefix(timestamp, []byte(`time="`)) && timestamp[len(timestamp)-1] == '"' {
			switch {
			case bytes.HasPrefix(fields, []byte(`level=info msg="[TUN] Tun adapter listening at: `+tunDeviceName+"(")):
				close(l.ready)
				l.reported = true
			case bytes.HasPrefix(fields, []byte(tunErrorPrefix)):
				l.failure <- classifyTUNCause(fields[len(tunErrorPrefix):])
				l.reported = true
			}
		}
		clear(l.prefix[:l.used])
		l.used = 0
		data = rest
	}
	return n, nil
}

const tunErrorPrefix = `level=error msg="Start TUN listening error: `

func classifyTUNCause(reason []byte) error {
	if cause, configuring := bytes.CutPrefix(reason, []byte("configure tun interface: ")); configuring {
		switch {
		case bytes.HasPrefix(cause, []byte(`route ip+net: no such network interface"`)):
			return tunStartupError{"tun_core_interface_lookup", "代理核心在设置 TUN 网络规则时未找到 KeyDockTun 网卡。请重试，若仍失败，请从窗口底部导出诊断日志。"}
		case bytes.HasPrefix(cause, []byte("set ipv4 address:")), bytes.HasPrefix(cause, []byte("set ipv4 options:")):
			return tunStartupError{"tun_core_ipv4", "Windows 未能为 KeyDockTun 应用 IPv4 设置。请确认虚拟网卡已启用 IPv4，再重试。"}
		case bytes.HasPrefix(cause, []byte("set ipv4 dns:")):
			return tunStartupError{"tun_core_dns", "Windows 未能设置 KeyDockTun 的 DNS。请导出诊断日志，让管理员检查此网卡的 DNS 策略。"}
		case bytes.HasPrefix(cause, []byte("FwpmEngineOpen0:")), bytes.HasPrefix(cause, []byte("FwpmSubLayerAdd0:")), bytes.HasPrefix(cause, []byte("FwpmFilterAdd0:")):
			return tunStartupError{"tun_core_firewall", "Windows 筛选平台未能应用 TUN 网络规则。请让管理员检查基础筛选引擎服务（BFE）和网络策略。"}
		default:
			return tunStartupError{"tun_core_adapter", "Windows 未能完成 KeyDockTun 虚拟网卡初始化。请导出诊断日志，让管理员检查 Wintun 设备状态。"}
		}
	}
	switch {
	case bytes.HasPrefix(reason, []byte("create NetworkUpdateMonitor:")), bytes.HasPrefix(reason, []byte("start NetworkUpdateMonitor:")),
		bytes.HasPrefix(reason, []byte("create DefaultInterfaceMonitor:")), bytes.HasPrefix(reason, []byte("start DefaultInterfaceMonitor:")):
		return tunStartupError{"tun_core_monitor", "代理核心未能监测 Windows 网络接口。请检查当前网络连接后重试。"}
	default:
		return tunStartupError{"tun_core_initialization", "代理核心未能启动 TUN。请在窗口底部导出诊断日志，交给管理员排查。"}
	}
}
