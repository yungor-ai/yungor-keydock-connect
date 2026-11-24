package proxy

import "sync"

type Manager struct {
	mu                 sync.Mutex
	scratch            sync.RWMutex
	stateDir, corePath string
	running            *child
}

func New(stateDir, corePath string) *Manager { return &Manager{stateDir: stateDir, corePath: corePath} }
func (m *Manager) Active() bool              { m.mu.Lock(); defer m.mu.Unlock(); return m.running != nil }
