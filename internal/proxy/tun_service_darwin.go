//go:build darwin && arm64 && cgo

package proxy

/*
#cgo CFLAGS: -fobjc-arc -fblocks
#cgo LDFLAGS: -framework Foundation -framework Security -framework SystemConfiguration
#include <stdbool.h>
#include <stdlib.h>
#include <sys/types.h>
char *kdTUNCopyRequirement(void);
bool kdTUNConsoleUser(uid_t);
void *kdTUNListen(const char *);
void kdTUNStopListening(void *);
int kdTUNRequest(int, const char *);
*/
import "C"

import (
	"context"
	"errors"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type tunRequest struct {
	fd  int
	uid C.uid_t
}

var (
	// Only the signature-checked XPC listener hands leases to this channel.
	tunRequests         = make(chan tunRequest, 1)
	tunRequestsMu       sync.Mutex
	tunServiceRetiring  bool
	errDarwinTUNRestart = errors.New("KeyDock 网络后台服务正在切换到新版，请稍后重新启用代理")
)

//export kdTUNEnqueue
func kdTUNEnqueue(fd C.int, uid C.uid_t) C.bool {
	tunRequestsMu.Lock()
	defer tunRequestsMu.Unlock()
	if tunServiceRetiring {
		return false
	}
	select {
	case tunRequests <- tunRequest{fd: int(fd), uid: uid}:
		return true
	default:
		return false
	}
}

func requestDarwinTUNChannel() (*net.UnixConn, error) {
	requirement := C.kdTUNCopyRequirement()
	if requirement == nil {
		return nil, errors.New("网络后台服务需要正式签名和公证的 KeyDock Connect 安装包")
	}
	defer C.free(unsafe.Pointer(requirement))

	syscall.ForkLock.RLock()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == nil {
		unix.CloseOnExec(fds[0])
		unix.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, errors.New("无法创建网络服务会话通道")
	}
	peer := os.NewFile(uintptr(fds[1]), "tun-peer")
	defer peer.Close()
	conn, err := tunFileConn(fds[0])
	if err != nil {
		return nil, err
	}
	if C.kdTUNRequest(C.int(peer.Fd()), requirement) != 1 {
		_ = conn.Close()
		return nil, errors.New("无法连接 KeyDock 网络后台服务，请稍后重试。若仍失败，请检查后台项目授权并使用当前登录的 macOS 会话")
	}
	return conn, nil
}

func tunFileConn(fd int) (*net.UnixConn, error) {
	file := os.NewFile(uintptr(fd), "tun-session")
	defer file.Close()
	kind, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || kind != unix.SOCK_STREAM {
		return nil, errors.New("无效的网络服务会话通道")
	}
	raw, err := net.FileConn(file)
	if err != nil {
		return nil, errors.New("无法打开网络服务会话通道")
	}
	conn, ok := raw.(*net.UnixConn)
	if !ok {
		_ = raw.Close()
		return nil, errors.New("无效的网络服务会话通道")
	}
	return conn, nil
}

func runDarwinTUNService() error {
	if os.Geteuid() != 0 {
		return errors.New("网络后台服务需要 macOS 管理员授权")
	}
	requirement := C.kdTUNCopyRequirement()
	if requirement == nil {
		return errors.New("网络后台服务需要正式签名的 KeyDock Connect 安装包")
	}
	defer C.free(unsafe.Pointer(requirement))
	executable, err := os.Executable()
	if err != nil || !filepath.IsAbs(executable) {
		return errors.New("无法定位内置代理核心")
	}
	original, err := os.Stat(executable)
	if err != nil {
		return errors.New("无法定位网络后台服务程序")
	}
	corePath := filepath.Join(filepath.Dir(executable), "..", "Resources", "core", "mihomo")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	listener := C.kdTUNListen(requirement)
	if listener == nil {
		return errors.New("无法启动 KeyDock 网络后台服务")
	}
	defer C.kdTUNStopListening(listener)
	// A daemon killed mid-session may have left macOS pointing at a closed
	// listener. Restore its root-owned preferences receipt before accepting work.
	if err := updateDarwinSystemProxy(false); err != nil {
		return err
	}
	var lease sync.Mutex
	var workers sync.WaitGroup
	defer workers.Wait()
	replacementCheck := time.NewTicker(2 * time.Second)
	defer replacementCheck.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-replacementCheck.C:
			if lease.TryLock() {
				// Retry restoration after transient settings lock / apply failures.
				_ = updateDarwinSystemProxy(false)
				changed := darwinTUNExecutableChanged(executable, original)
				lease.Unlock()
				if changed {
					retireDarwinTUNService()
					return nil
				}
			}
		case request := <-tunRequests:
			conn, err := tunFileConn(request.fd)
			if err != nil {
				continue
			}
			if !lease.TryLock() {
				finishDarwinTUNLease(conn, errors.New("另一个会话正在使用 KeyDock 网络服务，请先停用该会话的代理"))
				_ = conn.Close()
				continue
			}
			if darwinTUNExecutableChanged(executable, original) {
				lease.Unlock()
				retireDarwinTUNService()
				finishDarwinTUNLease(conn, errDarwinTUNRestart)
				_ = conn.Close()
				return nil
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				session, cancel := context.WithCancel(ctx)
				defer cancel()
				go watchTUNConsole(session, request.uid, cancel)
				err := serveDarwinTUNLease(session, conn, func(ctx context.Context, body []byte, ready func() error) error {
					if darwinTUNExecutableChanged(executable, original) {
						return errDarwinTUNRestart
					}
					return serveRegisteredDarwinService(ctx, os.Args[1:], corePath, body, ready)
				})
				// The session is free before teardown is acknowledged.
				lease.Unlock()
				finishDarwinTUNLease(conn, err)
			}()
		}
	}
}

func probeDarwinRegistration() int {
	requirement := C.kdTUNCopyRequirement()
	if requirement == nil {
		return 0
	}
	defer C.free(unsafe.Pointer(requirement))
	return int(C.kdTUNRequest(-1, requirement))
}

func runDarwinNetworkService(ctx context.Context, corePath string, body []byte, ready func() error) error {
	// Clear a prior lease's retryable cleanup before another mode is admitted.
	if err := updateDarwinSystemProxy(false); err != nil {
		return err
	}
	if string(body) == darwinSystemProxyRequest {
		return runDarwinSystemProxy(ctx, ready)
	}
	return runDarwinTUN(ctx, corePath, body, ready)
}

func darwinTUNExecutableChanged(path string, original os.FileInfo) bool {
	current, err := os.Stat(path)
	return err != nil || !os.SameFile(original, current)
}

// Stop accepting requests before withdrawing the XPC listener. launchd will
// start the installed executable on demand after this daemon exits.
func retireDarwinTUNService() {
	tunRequestsMu.Lock()
	tunServiceRetiring = true
	tunRequestsMu.Unlock()
	select {
	case request := <-tunRequests:
		if conn, err := tunFileConn(request.fd); err == nil {
			finishDarwinTUNLease(conn, errDarwinTUNRestart)
			_ = conn.Close()
		}
	default:
	}
}

func watchTUNConsole(ctx context.Context, uid C.uid_t, cancel context.CancelFunc) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !bool(C.kdTUNConsoleUser(uid)) {
				cancel()
				return
			}
		}
	}
}

func serveDarwinTUNLease(parent context.Context, conn net.Conn, run func(context.Context, []byte, func() error) error) error {
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	if err := writeTUNFrame(conn, nil); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	body, err := readTUNFrame(conn, profileLimit)
	if err != nil || len(body) == 0 {
		return errors.New("网络服务请求无效或超出大小限制")
	}
	_ = conn.SetReadDeadline(time.Time{})
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() {
		var stop [1]byte
		_, _ = conn.Read(stop[:])
		cancel()
	}()
	started := false
	err = run(ctx, body, func() error {
		_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		err := writeTUNFrame(conn, nil)
		started = err == nil
		return err
	})
	if err == nil && !started {
		return errors.New("网络服务在就绪前停止")
	}
	return err
}

func finishDarwinTUNLease(conn net.Conn, err error) {
	var message []byte
	if err != nil {
		message = []byte(err.Error())
		if len(message) > tunReplyLimit {
			message = []byte("网络后台服务报告运行失败")
		}
	}
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_ = writeTUNFrame(conn, message)
}
