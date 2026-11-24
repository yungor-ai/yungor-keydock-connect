package proxy

import (
	"encoding/json"
	"errors"
	"github.com/yungor-ai/yungor-keydock-connect/internal/codex"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	wininet     = windows.NewLazySystemDLL("wininet.dll")
	queryOption = wininet.NewProc("InternetQueryOptionW")
	setOption   = wininet.NewProc("InternetSetOptionW")
	globalFree  = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalFree")
)

type connOption struct {
	ID    uint32
	Value uintptr
}
type connList struct {
	Size        uint32
	Connection  *uint16
	Count       uint32
	OptionError uint32
	Options     *connOption
}

const perConnectionOption = 75

func currentSettings() (settings, error) {
	opts := [3]connOption{{ID: 10}, {ID: 2}, {ID: 3}} // FLAGS_UI, PROXY_SERVER, PROXY_BYPASS
	list := connList{Count: 3, Options: &opts[0]}
	list.Size = uint32(unsafe.Sizeof(list))
	size := list.Size
	ok, _, _ := queryOption.Call(0, perConnectionOption, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&size)))
	read := func(o *connOption) string {
		ptr := *(**uint16)(unsafe.Pointer(&o.Value))
		if ptr == nil {
			return ""
		}
		defer globalFree.Call(o.Value)
		return windows.UTF16PtrToString(ptr)
	}
	server, bypass := read(&opts[1]), read(&opts[2])
	runtime.KeepAlive(&list)
	if ok == 0 {
		return settings{}, errors.New("无法读取 Windows 当前用户代理设置")
	}
	return settings{Flags: uint32(opts[0].Value), Server: server, Bypass: bypass}, nil
}

func applySettings(s settings) error {
	server, e1 := windows.UTF16PtrFromString(s.Server)
	bypass, e2 := windows.UTF16PtrFromString(s.Bypass)
	if e1 != nil || e2 != nil {
		return errors.New("Windows 代理设置无效")
	}
	opts := [3]connOption{{ID: 1, Value: uintptr(s.Flags)}, {ID: 2, Value: uintptr(unsafe.Pointer(server))}, {ID: 3, Value: uintptr(unsafe.Pointer(bypass))}}
	list := connList{Count: 3, Options: &opts[0]}
	list.Size = uint32(unsafe.Sizeof(list))
	ok, _, _ := setOption.Call(0, perConnectionOption, uintptr(unsafe.Pointer(&list)), uintptr(list.Size))
	runtime.KeepAlive(server)
	runtime.KeepAlive(bypass)
	runtime.KeepAlive(&opts)
	if ok == 0 {
		return errors.New("Windows 未接受当前用户代理设置")
	}
	changed, _, _ := setOption.Call(0, 39, 0, 0) // INTERNET_OPTION_SETTINGS_CHANGED
	refresh, _, _ := setOption.Call(0, 37, 0, 0) // INTERNET_OPTION_REFRESH
	if changed == 0 || refresh == 0 {
		return errors.New("Windows 代理设置已写入，但刷新失败")
	}
	return nil
}

func (m *Manager) journalPath() string { return filepath.Join(m.stateDir, "system-proxy.dpapi") }
func (m *Manager) record(j ownership) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	data, err = codex.Protect(data)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(m.stateDir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(m.stateDir, ".proxy-restore-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	return os.Rename(f.Name(), m.journalPath())
}
func (m *Manager) Recover() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running != nil {
		return nil
	}
	err := m.recoverLocked()
	m.pruneRuntime()
	return err
}
func (m *Manager) recoverLocked() error {
	data, err := os.ReadFile(m.journalPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err = codex.Unprotect(data)
	if err != nil {
		return err
	}
	var j ownership
	if json.Unmarshal(data, &j) != nil || j.Owned.Server == "" {
		return errors.New("Windows 代理恢复记录已损坏")
	}
	now, err := currentSettings()
	if err != nil {
		return err
	}
	if replacement, needed := j.restore(now); needed {
		if err = applySettings(replacement); err != nil {
			return err
		}
	}
	return os.Remove(m.journalPath())
}
