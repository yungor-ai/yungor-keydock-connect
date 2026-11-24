package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
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

var geoFiles = map[string]string{
	"GeoSite.dat":  "2bdf171e1862b47cc5cd322ee43e5d066d9963025919ebb58dc7891d2b92643e",
	"geoip.metadb": "4236cf8477d12ceb4837eff39a03a65d5d55a72d3cccdc92e2e36d9cdbd686e3",
}

type child struct {
	job     windows.Handle
	cmd     *exec.Cmd
	done    chan struct{}
	dataDir string
}

func digestCopy(path string, out io.Writer, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("缺少内置代理核心或离线分流数据，请重新安装")
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(io.MultiWriter(out, h), f); err != nil || hex.EncodeToString(h.Sum(nil)) != expected {
		return errors.New("代理组件校验失败，请重新安装")
	}
	return nil
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
func (m *Manager) launch(ctx context.Context, profile []byte, port int) (*child, error) {
	if err := digestCopy(m.corePath, io.Discard, coreDigest); err != nil {
		return nil, err
	}
	dir, err := m.dataDir()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(m.corePath, "-d", dir, "-f", "-")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	cmd.Env = []string{}
	for _, k := range []string{"SystemRoot", "WINDIR", "LOCALAPPDATA", "USERPROFILE", "TEMP", "TMP"} {
		if v := os.Getenv(k); v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	// Keep stdin open until the job owns the process. No credentials are stored
	// on the command line, in temporary files or in proxy logs.
	in, err := cmd.StdinPipe()
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		in.Close()
		os.RemoveAll(dir)
		return nil, errors.New("Windows 无法启动内置代理核心")
	}
	job, err := restrictJob(cmd)
	if err != nil {
		cmd.Process.Kill()
		in.Close()
		cmd.Wait()
		os.RemoveAll(dir)
		return nil, errors.New("无法启用 Windows 代理进程保护")
	}
	p := &child{cmd: cmd, job: job, dataDir: dir, done: make(chan struct{})}
	go func() { cmd.Wait(); close(p.done) }()
	// The pinned child reads stdin before loading the network stack.
	go func() { in.Write(profile); in.Close() }()
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			p.close()
			return nil, ctx.Err()
		case <-p.done:
			p.close()
			return nil, errors.New("代理核心未能加载订阅分流配置")
		case <-deadline.C:
			p.close()
			return nil, errors.New("内置代理未能及时启动")
		case <-ticker.C:
			if ownsListener(cmd.Process.Pid, port) {
				return p, nil
			}
		}
	}
}

func (p *child) close() {
	windows.CloseHandle(p.job)
	<-p.done
	os.RemoveAll(p.dataDir)
}

func (m *Manager) Start(raw string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running != nil {
		return nil
	}
	if err := m.recoverLocked(); err != nil {
		return err
	}
	m.pruneRuntime()
	body, err := subscription(context.Background(), raw, nil)
	if err != nil {
		return err
	}
	return m.startProfileLocked(body)
}

func loopbackPort() (int, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, errors.New("无法分配本机代理端口")
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func (m *Manager) startProfileLocked(body []byte) error {
	port, err := loopbackPort()
	if err != nil {
		return err
	}
	cfg, err := buildProfile(body, port)
	if err != nil {
		return err
	}
	p, err := m.launch(context.Background(), cfg, port)
	if err != nil {
		return err
	}
	original, err := currentSettings()
	if err != nil {
		p.close()
		return err
	}
	next := settings{Flags: 3, Server: "127.0.0.1:" + strconv.Itoa(port), Bypass: "<local>;localhost;127.*;[::1]"}
	if err = m.record(ownership{Prior: original, Owned: next}); err != nil {
		p.close()
		return err
	}
	if err = applySettings(next); err == nil {
		var actual settings
		actual, err = currentSettings()
		if err == nil && (actual.Server != next.Server || actual.Flags&2 == 0) {
			err = errors.New("Windows 策略阻止了代理设置")
		}
	}
	if err != nil {
		restoreErr := m.recoverLocked()
		p.close()
		return errors.Join(err, restoreErr)
	}
	m.running = p
	go func() {
		<-p.done
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.running != p {
			return
		}
		// Stop() or Recover() retries a failed restoration using the journal.
		_ = m.recoverLocked()
		m.running = nil
		p.close()
	}()
	return nil
}
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	err := m.recoverLocked()
	if m.running != nil {
		p := m.running
		m.running = nil
		p.close()
	}
	return err
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
	body, err := subscription(ctx, subscriptionURL, tlsConfig)
	if err != nil {
		return err
	}

	m.scratch.RLock()
	defer m.scratch.RUnlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	port, err := loopbackPort()
	if err != nil {
		return err
	}
	data, err := buildRouting(body, port, true)
	if err != nil {
		return err
	}
	p, err := m.launch(ctx, data, port)
	if err != nil {
		return err
	}
	defer p.close()
	local := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	tr := &http.Transport{Proxy: http.ProxyURL(local), TLSClientConfig: tlsConfig, DisableKeepAlives: true, TLSHandshakeTimeout: 8 * time.Second}
	defer tr.CloseIdleConnections()
	c := http.Client{Transport: tr, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return errors.New("代理检查地址无效")
	}
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("SS2022 代理未能连接到 HTTPS 测试服务")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return errors.New("代理测试服务未返回预期的 204 响应")
	}
	return nil
}
