//go:build darwin && arm64

package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const localPort = 17890

type localProfile struct {
	port         int
	body, config []byte
}

type child struct {
	mode        Mode
	profile     localProfile
	tun         *darwinTUNLease
	systemProxy *darwinTUNLease
	lifeline    *os.File
	cmd         *exec.Cmd
	started     chan int
	done        chan struct{}
	waitErr     error // Set before done closes.
	activeAt    time.Time
	groupMu     sync.Mutex
	reaping     bool
}

func (m *Manager) ProxyAddress() string {
	if p := m.active.Load(); p != nil && (p.mode == ModeLocal || p.mode == ModeSystem) {
		return localAddress(p.profile.port)
	}
	return ""
}

func localAddress(port int) string {
	return "127.0.0.1:" + strconv.Itoa(port)
}

func (m *Manager) CheckMode(mode Mode) error {
	if mode != ModeLocal && mode != ModeSystem && mode != ModeTUN {
		return errors.New("无效的 macOS 代理模式")
	}
	return nil
}

func (m *Manager) Start(raw string) error { return m.StartMode(context.Background(), raw, ModeSystem) }

func (m *Manager) StartMode(ctx context.Context, raw string, mode Mode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.CheckMode(mode); err != nil {
		return err
	}
	if mode != ModeTUN {
		return m.startDarwinProxy(ctx, raw, nil, localPort, mode)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.beforeDarwinStartLocked(); err != nil {
		return err
	}
	if m.running != nil {
		if m.running.mode == mode {
			return nil
		}
		return errors.New("请先停用当前代理，再切换代理模式")
	}
	profile, err := m.loadLocalProfile(ctx, raw, nil, localPort, false)
	if err != nil {
		return err
	}
	return m.startTUNProfileLocked(ctx, profile)
}

func (m *Manager) startLocal(ctx context.Context, raw string, tlsConfig *tls.Config, port int) error {
	return m.startDarwinProxy(ctx, raw, tlsConfig, port, ModeLocal)
}

func (m *Manager) startDarwinProxy(ctx context.Context, raw string, tlsConfig *tls.Config, port int, mode Mode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.beforeDarwinStartLocked(); err != nil {
		return err
	}
	if m.running != nil {
		if m.running.mode != mode {
			return errors.New("请先停用当前代理，再切换代理模式")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	started := time.Now()
	address := localAddress(port)
	tcp, err := net.Listen("tcp4", address)
	if err == nil {
		udp, udpErr := net.ListenPacket("udp4", address)
		tcp.Close()
		if udpErr == nil {
			udp.Close()
		}
		err = udpErr
	}
	m.logStage("proxy.local_port", started, err)
	if err != nil {
		return fmt.Errorf("本机代理地址 %s 无法使用，请检查端口是否已被占用", address)
	}
	profile, err := m.loadLocalProfile(ctx, raw, tlsConfig, port, false)
	if err != nil {
		return err
	}
	if mode == ModeSystem {
		return m.startSystemProfileLocked(ctx, profile)
	}
	return m.startLocalProfileLocked(ctx, profile)
}

func (m *Manager) loadLocalProfile(ctx context.Context, raw string, tlsConfig *tls.Config, port int, diagnostic bool) (localProfile, error) {
	body, err := m.subscription(ctx, raw, tlsConfig)
	if err != nil {
		return localProfile{}, err
	}
	started := time.Now()
	cfg, err := buildRoutingProfile(body, port, diagnostic, ModeLocal)
	m.logStage("proxy.profile", started, err)
	return localProfile{port: port, body: body, config: cfg}, err
}

func (m *Manager) startLocalProfileLocked(ctx context.Context, profile localProfile) error {
	p, err := m.launchLocal(ctx, profile, "run")
	if err != nil {
		return err
	}
	m.trackDarwinChild(p)
	return nil
}

// The daemon only points macOS at the fixed listener after this process has
// verified that the bundled core owns it. Either side ending tears down both.
func (m *Manager) startSystemProfileLocked(ctx context.Context, profile localProfile) error {
	p, err := m.launchLocal(ctx, profile, "run")
	if err != nil {
		return err
	}
	p.systemProxy, err = openDarwinTUN(ctx, []byte(darwinSystemProxyRequest))
	if err != nil {
		return errors.Join(fmt.Errorf("未能启用系统代理，请稍后重试以连接最新版后台服务：%w", err), closeLocalChild(p))
	}
	p.mode = ModeSystem
	m.trackDarwinChild(p)
	return nil
}

func (m *Manager) startTUNProfileLocked(ctx context.Context, profile localProfile) (result error) {
	started := time.Now()
	defer func() { m.logStage("proxy.tun_ready", started, result) }()
	lease, err := openDarwinTUN(ctx, profile.body)
	if err != nil {
		return err
	}
	m.trackDarwinChild(&child{mode: ModeTUN, profile: profile, tun: lease, done: lease.done})
	return nil
}

func (m *Manager) trackDarwinChild(p *child) {
	p.activeAt = time.Now()
	m.running = p
	m.active.Store(p)
	go func() {
		var settingsDone <-chan struct{}
		if p.systemProxy != nil {
			settingsDone = p.systemProxy.done
		}
		select {
		case <-p.done:
		case <-settingsDone:
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.running == p {
			m.noteDarwinWorkerExitLocked(p)
		}
	}()
}

func (m *Manager) noteDarwinWorkerExitLocked(p *child) {
	var waitErr error
	if p.systemProxy != nil {
		waitErr = errors.Join(p.systemProxy.stop(), closeLocalChild(p))
	}
	select {
	case <-p.done:
		waitErr = errors.Join(waitErr, p.waitErr)
	default:
	}
	if p.tun != nil {
		waitErr = p.tun.err
	}
	m.diagnostics.Record("proxy.worker_exit", "failed", time.Since(p.activeAt), waitErr, 0)
	m.clearDarwinChildLocked(p, waitErr)
	if p.lifeline != nil {
		p.lifeline.Close()
	}
}

func (m *Manager) clearDarwinChildLocked(p *child, err error) {
	if p.tun != nil && err != nil {
		m.pendingTUN = p
	}
	m.running = nil
	m.active.Store(nil)
}

func (m *Manager) beforeDarwinStartLocked() error {
	// A request can acquire the lock before the worker's exit watcher does.
	if p := m.running; p != nil && p.tun != nil {
		select {
		case <-p.done:
			m.noteDarwinWorkerExitLocked(p)
		default:
		}
	}
	return m.recheckDarwinTUNCleanupLocked(waitDarwinTUNCleanup)
}

func (m *Manager) recheckDarwinTUNCleanupLocked(recheck func() error) error {
	if m.pendingTUN == nil {
		return nil
	}
	if err := recheck(); err != nil {
		return err
	}
	m.pendingTUN = nil
	return nil
}

func (m *Manager) spawnLocal(profile localProfile, purpose string) (_ *child, result error) {
	started := time.Now()
	defer func() { m.logStage("proxy.core_process", started, result) }()
	core, err := filepath.Abs(m.corePath)
	if err != nil {
		return nil, errors.New("无法定位内置代理核心")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, errors.New("无法查找接入助手程序")
	}
	if err = os.MkdirAll(m.stateDir, 0700); err != nil {
		return nil, errors.New("无法创建私有代理运行目录")
	}
	parent, lifeline, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	ready, notify, err := os.Pipe()
	if err != nil {
		lifeline.Close()
		return nil, err
	}
	defer notify.Close()
	cmd := exec.Command(executable, "--proxy-worker", core, strconv.Itoa(profile.port), purpose)
	cmd.Stdin = bytes.NewReader(profile.body)
	cmd.Stdout = notify
	cmd.ExtraFiles = []*os.File{parent}
	cmd.Env = []string{"TMPDIR=" + m.stateDir}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err = cmd.Start(); err != nil {
		lifeline.Close()
		ready.Close()
		return nil, errors.New("无法启动本机代理进程")
	}
	p := &child{mode: ModeLocal, profile: profile, lifeline: lifeline, cmd: cmd, started: make(chan int, 1), done: make(chan struct{})}
	var runtimeName string
	reported := make(chan struct{})
	go func() {
		clean, exitErr := waitLocalWorkerExit(cmd.Process.Pid)
		killErr := p.beforeReap(clean)
		waitErr := cmd.Wait()
		<-reported
		var cleanupErr error
		if localRuntimeName(runtimeName) {
			cleanupErr = os.RemoveAll(filepath.Join(m.stateDir, runtimeName))
		}
		p.waitErr = errors.Join(exitErr, killErr, waitErr, cleanupErr)
		close(p.done)
	}()
	go func() {
		defer ready.Close()
		defer close(reported)
		var pid int
		if _, err := fmt.Fscan(ready, &pid); err == nil && pid > 0 {
			_, _ = fmt.Fscan(ready, &runtimeName)
		}
		p.started <- pid
	}()
	return p, nil
}

// Observe exit without reaping our child. Its PID remains reserved while we
// finish the private group, including a core orphaned by a forced worker exit.
func waitLocalWorkerExit(pid int) (clean bool, err error) {
	queue, err := unix.Kqueue()
	if err != nil {
		return false, err
	}
	defer unix.Close(queue)
	unix.CloseOnExec(queue)
	changes := []unix.Kevent_t{{
		Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT | unix.NOTE_EXITSTATUS,
	}}
	for {
		_, err = unix.Kevent(queue, changes, nil, nil)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if errors.Is(err, unix.ESRCH) {
		return false, nil // The child already exited, but has not been reaped.
	}
	if err != nil {
		return false, err
	}
	var events [1]unix.Kevent_t
	for {
		n, err := unix.Kevent(queue, nil, events[:], nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		// On an ordinary exit the worker already waited on Mihomo, including
		// when it reported a configuration error. Avoid signaling an exit
		// that is still being finalized by the kernel.
		return n == 1 && events[0].Fflags&unix.NOTE_EXITSTATUS != 0 &&
			unix.WaitStatus(events[0].Data).Exited(), err
	}
}

func (p *child) beforeReap(clean bool) error {
	p.groupMu.Lock()
	defer p.groupMu.Unlock()
	// cmd.Wait is called exactly once, after this guard has prevented any
	// further group signals. A recycled PID can never be a target here.
	p.reaping = true
	if clean {
		return nil
	}
	return killLocalGroup(p.cmd.Process.Pid)
}

func (p *child) killTimedOutGroup() error {
	p.groupMu.Lock()
	defer p.groupMu.Unlock()
	if p.reaping {
		return nil
	}
	return killLocalGroup(p.cmd.Process.Pid)
}

func killLocalGroup(pid int) error {
	if err := unix.Kill(-pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
		return errors.New("无法结束本机代理进程")
	}
	return nil
}

func (m *Manager) launchLocal(ctx context.Context, profile localProfile, purpose string) (_ *child, result error) {
	p, err := m.spawnLocal(profile, purpose)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	defer func() { m.logStage("proxy.core_ready", started, result) }()
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	fail := func(err error) (*child, error) {
		return nil, errors.Join(err, closeLocalChild(p))
	}
	var pid int
	select {
	case pid = <-p.started:
		if pid < 1 {
			code := -pid
			if code >= len(workerFailures) {
				code = 0
			}
			return fail(errors.New(workerFailures[code]))
		}
	case <-ctx.Done():
		return fail(ctx.Err())
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ownsDarwinListener(ctx, pid, profile.port) {
			return p, nil
		}
		select {
		case <-p.done:
			return fail(errors.New("代理核心未能加载订阅分流配置"))
		case <-ctx.Done():
			return fail(errors.New("内置代理未能及时启动或端口已被占用"))
		case <-ticker.C:
		}
	}
}

// Confirm the active listener belongs to the worker before displaying its
// address. Simply probing a TCP port would accept an unrelated service.
func ownsDarwinListener(ctx context.Context, pid, port int) bool {
	cmd := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", "-a", "-p", strconv.Itoa(pid),
		"-iTCP@"+localAddress(port), "-sTCP:LISTEN", "-t")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == strconv.Itoa(pid)
}

func closeLocalChild(p *child) error {
	p.lifeline.Close()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-p.done:
		return nil
	case <-timer.C:
		if err := p.killTimedOutGroup(); err != nil {
			return err
		}
	}
	timer.Reset(time.Second)
	select {
	case <-p.done:
		return nil
	case <-timer.C:
		return errors.New("本机代理进程未能及时退出")
	}
}

func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopDarwinLocked()
}

func (m *Manager) stopDarwinLocked() error {
	if err := m.recheckDarwinTUNCleanupLocked(waitDarwinTUNCleanup); err != nil {
		return err
	}
	p := m.running
	if p == nil {
		return nil
	}
	var err error
	if p.tun != nil {
		err = p.tun.stop()
	} else {
		if p.systemProxy != nil {
			err = p.systemProxy.stop()
		}
		if closeErr := closeLocalChild(p); closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	m.clearDarwinChildLocked(p, err)
	return err
}

func (m *Manager) Recover() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scratch.Lock()
	defer m.scratch.Unlock()
	if m.running != nil {
		return nil
	}
	entries, err := os.ReadDir(m.stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() && localRuntimeName(e.Name()) {
			if err = os.RemoveAll(filepath.Join(m.stateDir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func localRuntimeName(name string) bool {
	suffix, ok := strings.CutPrefix(name, runtimePrefix)
	_, err := strconv.ParseUint(suffix, 10, 64)
	return ok && err == nil
}

func (m *Manager) Refresh(ctx context.Context, raw string) error {
	return m.refresh(ctx, raw, nil)
}

func (m *Manager) refresh(ctx context.Context, raw string, tlsConfig *tls.Config) (result error) {
	started := time.Now()
	defer func() { m.logStage("proxy.refresh", started, result) }()
	prior := m.active.Load()
	if prior == nil {
		return errors.New("代理已关闭，此次未更新订阅")
	}
	profile, err := m.loadLocalProfile(ctx, raw, tlsConfig, prior.profile.port, false)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running != prior {
		return errors.New("代理状态已变化，请重试刷新")
	}
	if bytes.Equal(prior.profile.config, profile.config) {
		return nil
	}
	if err = m.validateLocalProfile(ctx, profile); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	startProfile := m.startLocalProfileLocked
	switch prior.mode {
	case ModeTUN:
		startProfile = m.startTUNProfileLocked
	case ModeSystem:
		startProfile = m.startSystemProfileLocked
	}
	if err = m.stopDarwinLocked(); err != nil {
		return err
	}
	if err = startProfile(ctx, profile); err != nil {
		rollback := startProfile(context.Background(), prior.profile)
		if rollback != nil {
			return errors.Join(errors.New("旧代理配置恢复失败"), err, rollback)
		}
		return errors.Join(errors.New("已恢复旧代理配置"), err)
	}
	return nil
}

func (m *Manager) validateLocalProfile(ctx context.Context, profile localProfile) error {
	p, err := m.spawnLocal(profile, "validate")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case <-p.done:
		if p.waitErr != nil {
			err = errors.New("内置代理无法加载更新后的订阅配置")
		}
	case <-ctx.Done():
		err = ctx.Err()
	}
	return errors.Join(err, closeLocalChild(p))
}

func (m *Manager) Test(ctx context.Context, raw string) error {
	return m.testConnection(ctx, raw, "https://www.gstatic.com/generate_204", nil)
}

func (m *Manager) testConnection(ctx context.Context, raw, target string, tlsConfig *tls.Config) (result error) {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	m.scratch.RLock()
	defer m.scratch.RUnlock()
	started := time.Now()
	port, err := loopbackPort()
	m.logStage("proxy.local_port", started, err)
	if err != nil {
		return err
	}
	profile, err := m.loadLocalProfile(ctx, raw, tlsConfig, port, true)
	if err != nil {
		return err
	}
	p, err := m.launchLocal(ctx, profile, "test")
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, closeLocalChild(p)) }()
	return m.checkHTTPS(ctx, port, target, tlsConfig)
}
