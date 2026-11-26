package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var tcpTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

const coreDigest = "5a0231811a80b27477ebdee15684b41946d01afbd2114534e9a4b256125ca147"

type child struct {
	job     windows.Handle
	cmd     *exec.Cmd
	done    chan struct{}
	dataDir string
	waitErr error // Set before done closes.
	mode    Mode
	port    int
	profile []byte
	tunLUID uint64
	tunLock windows.Handle
}

func (m *Manager) ProxyAddress() string {
	if p := m.active.Load(); p != nil && p.mode == ModeSystem {
		return "127.0.0.1:" + strconv.Itoa(p.port)
	}
	return ""
}

func openVerifiedCore(path, expected string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, errors.New("内置代理核心路径无效，请重新安装")
	}
	// Windows allows other readers, including the loader, but prevents this
	// file from being written, renamed or replaced while the handle is open.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_SEQUENTIAL_SCAN, 0)
	if err != nil {
		return nil, errors.New("无法锁定内置代理核心。请关闭更新程序后重试，或重新安装接入助手")
	}
	file := os.NewFile(uintptr(handle), path)
	if err := verifyDigest(file, io.Discard, expected); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func (m *Manager) dataDir() (string, error) {
	if err := os.MkdirAll(m.stateDir, 0700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(m.stateDir, "proxy-runtime-")
	if err != nil {
		return "", err
	}
	for name, hash := range geoFiles {
		out, e := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e == nil {
			e = digestCopy(filepath.Join(filepath.Dir(m.corePath), name), out, hash)
			closed := out.Close()
			if e == nil {
				e = closed
			}
		}
		if e != nil {
			os.RemoveAll(dir)
			return "", e
		}
	}
	return dir, nil
}

func restrictJob(cmd *exec.Cmd) (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err == nil {
		var proc windows.Handle
		proc, err = windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
		if err == nil {
			err = windows.AssignProcessToJobObject(job, proc)
			windows.CloseHandle(proc)
		}
	}
	if err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// TCP_TABLE_OWNER_PID_LISTENER returns MIB_TCPROW_OWNER_PID entries. Validate
// the socket's owning process before changing the user's network settings.
func ownsListener(pid, port int) bool {
	var size uint32
	tcpTable.Call(0, uintptr(unsafe.Pointer(&size)), 0, windows.AF_INET, 3, 0)
	if size < 4 || size > 16<<20 {
		return false
	}
	buf := make([]byte, size)
	rc, _, _ := tcpTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, windows.AF_INET, 3, 0)
	if rc != 0 || size < 4 {
		return false
	}
	count := int(binary.LittleEndian.Uint32(buf[:4]))
	const rowSize = 24
	for offset := 4; count > 0 && offset+rowSize <= len(buf); offset += rowSize {
		r := buf[offset : offset+rowSize]
		count--
		if binary.LittleEndian.Uint32(r) == 2 && bytes.Equal(r[4:8], []byte{127, 0, 0, 1}) && int(binary.BigEndian.Uint16(r[8:10])) == port && int(binary.LittleEndian.Uint32(r[20:24])) == pid {
			return true
		}
	}
	return false
}
func (m *Manager) spawnCore(profile []byte, mode Mode, verifyConfig bool) (*child, error) {
	started := time.Now()
	core, err := openVerifiedCore(m.corePath, coreDigest)
	m.logStage("proxy.core_integrity", started, err)
	if err != nil {
		return nil, err
	}
	defer core.Close()
	started = time.Now()
	dir, err := m.dataDir()
	m.logStage("proxy.core_data", started, err)
	if err != nil {
		return nil, err
	}
	args := []string{"-d", dir, "-f", "-"}
	if verifyConfig {
		args = append(args, "-t")
	}
	cmd := exec.Command(m.corePath, args...)
	cmd.Dir = filepath.Dir(m.corePath)
	flags := uint32(windows.CREATE_NO_WINDOW)
	if mode == ModeTUN {
		flags = windows.CREATE_NEW_CONSOLE
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
	cmd.Env = []string{}
	for _, k := range []string{"SystemRoot", "WINDIR", "LOCALAPPDATA", "USERPROFILE", "TEMP", "TMP"} {
		if v := os.Getenv(k); v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	// Keep stdin open until the job owns the process. No credentials are stored
	// on the command line, in temporary files or in proxy logs.
	started = time.Now()
	in, err := cmd.StdinPipe()
	if err != nil {
		m.logStage("proxy.core_process", started, err)
		os.RemoveAll(dir)
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		m.logStage("proxy.core_process", started, err)
		in.Close()
		os.RemoveAll(dir)
		return nil, errors.New("Windows 无法启动内置代理核心")
	}
	processElapsed := time.Since(started)
	started = time.Now()
	job, err := restrictJob(cmd)
	jobElapsed := time.Since(started)
	if err != nil {
		cmd.Process.Kill()
		in.Close()
		cmd.Wait()
		os.RemoveAll(dir)
		m.diagnostics.Record("proxy.core_process", "ok", processElapsed, nil, 0)
		m.diagnostics.Record("proxy.core_job", "failed", jobElapsed, err, 0)
		return nil, errors.New("无法启用 Windows 代理进程保护")
	}
	m.diagnostics.Record("proxy.core_process", "ok", processElapsed, nil, 0)
	m.diagnostics.Record("proxy.core_job", "ok", jobElapsed, nil, 0)
	p := &child{cmd: cmd, job: job, dataDir: dir, done: make(chan struct{}), mode: mode}
	go func() { p.waitErr = cmd.Wait(); close(p.done) }()
	// The pinned child reads stdin before loading the network stack.
	go func() { in.Write(profile); in.Close() }()
	return p, nil
}

func (m *Manager) launch(ctx context.Context, profile []byte, port int, mode Mode) (*child, error) {
	p, err := m.spawnCore(profile, mode, false)
	if err != nil {
		return nil, err
	}
	p.port, p.profile = port, profile
	started := time.Now()
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.logStage("proxy.core_ready", started, ctx.Err())
			return nil, errors.Join(ctx.Err(), m.closeChild(p))
		case <-p.done:
			m.diagnostics.Record("proxy.core_ready", "failed", time.Since(started), p.waitErr, 0)
			return nil, errors.Join(errors.New("代理核心未能加载订阅分流配置"), m.closeChild(p))
		case <-deadline.C:
			m.logStage("proxy.core_ready", started, context.DeadlineExceeded)
			return nil, errors.Join(errors.New("内置代理未能及时启动"), m.closeChild(p))
		case <-ticker.C:
			if ownsListener(p.cmd.Process.Pid, port) {
				m.logStage("proxy.core_ready", started, nil)
				if mode == ModeTUN {
					if err := m.awaitTUN(ctx, p); err != nil {
						return nil, errors.Join(err, m.closeChild(p))
					}
				}
				return p, nil
			}
		}
	}
}

func (m *Manager) closeChild(p *child) (resultErr error) {
	if p.mode == ModeTUN {
		defer func() {
			if resultErr != nil {
				m.pendingTUN = p
				return
			}
			m.pendingTUN = nil
			if p.tunLock != 0 {
				_ = windows.CloseHandle(p.tunLock)
				p.tunLock = 0
			}
		}()
		select {
		case <-p.done:
		default:
			p.requestTUNShutdown()
		}
	}
	var exitErr error
	if p.job != 0 {
		exitErr = windows.CloseHandle(p.job)
		if exitErr == nil {
			p.job = 0
		}
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
	case <-timer.C:
		return errors.Join(exitErr, errors.New("代理核心未能及时退出"))
	}
	_ = os.RemoveAll(p.dataDir)
	if p.mode != ModeTUN {
		return exitErr
	}
	started := time.Now()
	err := p.clearTUNRoutes()
	m.logStage("proxy.tun_cleanup", started, err)
	return errors.Join(exitErr, err)
}

func (m *Manager) Start(raw string) error { return m.StartMode(raw, ModeSystem) }

func (m *Manager) StartMode(raw string, mode Mode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.CheckMode(mode); err != nil {
		return err
	}
	if m.running != nil {
		if m.running.mode == mode {
			return nil
		}
		return errors.New("请先关闭当前代理，再切换代理模式")
	}
	if m.pendingTUN != nil {
		if err := m.closeChild(m.pendingTUN); err != nil {
			return err
		}
	}
	if err := m.recoverLocked(); err != nil {
		return err
	}
	m.pruneRuntime()
	ctx := context.Background()
	body, err := m.subscription(ctx, raw, nil)
	if err != nil {
		return err
	}
	return m.startSubscriptionLocked(ctx, body, mode)
}

func (m *Manager) startProfileLocked(body []byte) error {
	return m.startSubscriptionLocked(context.Background(), body, ModeSystem)
}

func (m *Manager) startSubscriptionLocked(ctx context.Context, body []byte, mode Mode) error {
	started := time.Now()
	port, err := loopbackPort()
	m.logStage("proxy.local_port", started, err)
	if err != nil {
		return err
	}
	started = time.Now()
	cfg, err := buildModeProfile(body, port, mode)
	m.logStage("proxy.profile", started, err)
	if err != nil {
		return err
	}
	return m.startModeProfileLocked(ctx, cfg, port, mode)
}

func (m *Manager) startModeProfileLocked(ctx context.Context, cfg []byte, port int, mode Mode) error {
	if mode == ModeTUN {
		lock, err := reserveTUN()
		if err != nil {
			return err
		}
		if err = m.startTUNLocked(ctx, cfg, port, lock); err != nil && m.pendingTUN == nil {
			_ = windows.CloseHandle(lock)
		}
		return err
	}
	p, err := m.launch(ctx, cfg, port, mode)
	if err != nil {
		return err
	}
	started := time.Now()
	err = m.applySystemProxy(port)
	m.logStage("proxy.settings_apply", started, err)
	if err != nil {
		return errors.Join(err, m.recoverLocked(), m.closeChild(p))
	}
	m.watchChild(p)
	return nil
}

func (m *Manager) startTUNLocked(ctx context.Context, cfg []byte, port int, lock windows.Handle) error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("启动 TUN 需要管理员权限")
	}
	if err := checkTUNStart(); err != nil {
		return err
	}
	p, err := m.launch(ctx, cfg, port, ModeTUN)
	if err != nil {
		if m.pendingTUN != nil {
			m.pendingTUN.tunLock = lock
		}
		return err
	}
	p.tunLock = lock
	m.watchChild(p)
	return nil
}

func (m *Manager) watchChild(p *child) {
	m.running = p
	m.active.Store(p)
	activeSince := time.Now()
	go func() {
		<-p.done
		lifetime := time.Since(activeSince)
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.running != p {
			return
		}
		m.diagnostics.Record("proxy.worker_exit", "failed", lifetime, p.waitErr, 0)
		if p.mode == ModeSystem {
			_ = m.recoverLocked()
		}
		m.running = nil
		_ = m.closeChild(p)
		m.active.Store(nil)
	}()
}

func (m *Manager) applySystemProxy(port int) error {
	original, err := currentSettings()
	if err != nil {
		return err
	}
	next := settings{Flags: 3, Server: "127.0.0.1:" + strconv.Itoa(port), Bypass: "<local>;localhost;127.*;[::1]"}
	if err = m.record(ownership{Prior: original, Owned: next}); err != nil {
		return err
	}
	if err = applySettings(next); err == nil {
		var actual settings
		actual, err = currentSettings()
		if err == nil && (actual.Server != next.Server || actual.Flags&2 == 0) {
			err = errors.New("Windows 策略阻止了代理设置")
		}
	}
	return err
}

func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopLocked()
}

func (m *Manager) stopLocked() error {
	if m.pendingTUN != nil {
		if err := m.closeChild(m.pendingTUN); err != nil {
			return err
		}
	}
	p := m.running
	if p == nil || p.mode == ModeSystem {
		if err := m.recoverLocked(); err != nil {
			return err
		}
	}
	if p == nil {
		return nil
	}
	m.running = nil
	err := m.closeChild(p)
	m.active.Store(nil)
	return err
}

func (m *Manager) Refresh(ctx context.Context, raw string) error {
	return m.refresh(ctx, raw, nil)
}

func (m *Manager) refresh(ctx context.Context, raw string, tlsConfig *tls.Config) (resultErr error) {
	started := time.Now()
	defer func() { m.logStage("proxy.refresh", started, resultErr) }()
	m.mu.Lock()
	prior := m.running
	m.mu.Unlock()
	if prior == nil {
		return errors.New("代理已关闭，此次未更新订阅")
	}
	body, err := m.subscription(ctx, raw, tlsConfig)
	if err != nil {
		return err
	}
	cfg, err := buildModeProfile(body, prior.port, prior.mode)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running != prior {
		return errors.New("代理状态已变化，请重试刷新")
	}
	if bytes.Equal(prior.profile, cfg) {
		return nil
	}
	if err = m.validateProfile(ctx, cfg); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return m.replaceProfileLocked(ctx, prior, cfg)
}

func (m *Manager) replaceProfileLocked(ctx context.Context, prior *child, cfg []byte) error {
	// Keep this process's TUN reservation across stop, start and rollback.
	lock := prior.tunLock
	prior.tunLock = 0
	if lock != 0 {
		defer func() {
			if m.pendingTUN != nil {
				m.pendingTUN.tunLock = lock
			} else if m.running == nil {
				_ = windows.CloseHandle(lock)
			}
		}()
	}
	// The Windows proxy retains the same port during reload. Reuse its
	// ownership journal and leave even concurrent user settings untouched.
	m.running = nil
	defer func() {
		if m.running == nil {
			m.active.Store(nil)
		}
	}()
	if err := m.closeChild(prior); err != nil {
		if prior.mode == ModeSystem {
			return errors.Join(err, m.recoverLocked())
		}
		return err
	}
	start := func(ctx context.Context, conf []byte) error {
		if prior.mode == ModeTUN {
			return m.startTUNLocked(ctx, conf, prior.port, lock)
		}
		p, err := m.launch(ctx, conf, prior.port, ModeSystem)
		if err != nil {
			return err
		}
		m.watchChild(p)
		return nil
	}
	if err := start(ctx, cfg); err != nil {
		if m.pendingTUN != nil {
			return errors.Join(err, errors.New("新 TUN 路由恢复后才能重新启用旧代理配置"))
		}
		rollback := start(context.Background(), prior.profile)
		if rollback != nil {
			if prior.mode == ModeSystem {
				rollback = errors.Join(rollback, m.recoverLocked())
			}
			return errors.Join(err, errors.New("旧代理配置恢复失败"), rollback)
		}
		return errors.Join(errors.New("已恢复旧代理配置"), err)
	}
	return nil
}

func (m *Manager) validateProfile(ctx context.Context, profile []byte) error {
	p, err := m.spawnCore(profile, ModeSystem, true)
	if err != nil {
		return err
	}
	validateCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case <-p.done:
		if p.waitErr != nil {
			err = errors.New("内置代理无法加载更新后的订阅配置")
		}
	case <-validateCtx.Done():
		err = validateCtx.Err()
	}
	return errors.Join(err, m.closeChild(p))
}

func (m *Manager) pruneRuntime() {
	m.scratch.Lock()
	defer m.scratch.Unlock()
	// This directory is private to KeyDock and the desktop holds a per-user
	// singleton lock. Remove orphaned data copies after an abnormal termination.
	entries, _ := os.ReadDir(m.stateDir)
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "proxy-runtime-") {
			suffix := strings.TrimPrefix(e.Name(), "proxy-runtime-")
			if _, err := strconv.ParseUint(suffix, 10, 64); err == nil {
				os.RemoveAll(filepath.Join(m.stateDir, e.Name()))
			}
		}
	}
}

// Test exercises the device subscription without changing WinINet or the managed
// desktop proxy. No API credential is sent to the connectivity-check endpoint.
func (m *Manager) Test(ctx context.Context, subscriptionURL string) error {
	return m.testConnection(ctx, subscriptionURL, "https://www.gstatic.com/generate_204", nil)
}

func (m *Manager) testConnection(ctx context.Context, subscriptionURL, target string, tlsConfig *tls.Config) error {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	body, err := m.subscription(ctx, subscriptionURL, tlsConfig)
	if err != nil {
		return err
	}

	m.scratch.RLock()
	defer m.scratch.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	started := time.Now()
	port, err := loopbackPort()
	m.logStage("proxy.local_port", started, err)
	if err != nil {
		return err
	}
	started = time.Now()
	data, err := buildRouting(body, port, true)
	m.logStage("proxy.profile", started, err)
	if err != nil {
		return err
	}
	p, err := m.launch(ctx, data, port, ModeSystem)
	if err != nil {
		return err
	}
	defer m.closeChild(p)
	return m.checkHTTPS(ctx, port, target, tlsConfig)
}
