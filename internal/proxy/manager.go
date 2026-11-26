package proxy

import (
	"fmt"
	"math/rand/v2"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yungor-ai/yungor-keydock-connect/internal/diagnostics"
)

type Mode string

const (
	ModeSystem Mode = "system"
	ModeLocal  Mode = "local"
	ModeTUN    Mode = "tun"
)

type Manager struct {
	mu                 sync.Mutex
	scratch            sync.RWMutex
	stateDir, corePath string
	running            *child
	active             atomic.Pointer[child]
	pendingTUN         *child
	diagnostics        *diagnostics.Log
}

func New(stateDir, corePath string) *Manager { return &Manager{stateDir: stateDir, corePath: corePath} }
func (m *Manager) Active() bool              { return m.ActiveMode() != "" }

func (m *Manager) ActiveMode() Mode {
	p := m.active.Load()
	if p == nil {
		return ""
	}
	return p.mode
}

// Mihomo's mixed listener needs the same port for TCP and UDP. Windows
// can reserve a port range for one transport independently of the other.
func loopbackPort() (int, error) {
	loopback := net.IPv4(127, 0, 0, 1)
	var lastErr error
	for attempt := range 32 {
		port := 0
		// UDP's automatic allocation skips its excluded ports. If those
		// choices conflict with TCP, spread retries outside the default
		// Windows dynamic port range instead of scanning adjacent ports.
		if attempt >= 8 {
			port = 20000 + rand.IntN(49152-20000)
		}
		udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: loopback, Port: port})
		if err != nil {
			lastErr = err
			continue
		}
		port = udp.LocalAddr().(*net.UDPAddr).Port
		tcp, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: loopback, Port: port})
		udp.Close()
		if err != nil {
			lastErr = err
			continue
		}
		tcp.Close()
		return port, nil
	}
	return 0, fmt.Errorf("无法分配本机代理端口：%w", lastErr)
}

// SetDiagnostics attaches the app log before the manager starts any operations.
func (m *Manager) SetDiagnostics(log *diagnostics.Log) { m.diagnostics = log }

func (m *Manager) logStage(stage string, started time.Time, err error) {
	m.logHTTPStage(stage, time.Since(started), err, 0, 0)
}

func (m *Manager) logHTTPStage(stage string, elapsed time.Duration, err error, status, want int) {
	outcome := "ok"
	if err != nil || status != want {
		outcome = "failed"
	}
	m.diagnostics.Record(stage, outcome, elapsed, err, status)
}
