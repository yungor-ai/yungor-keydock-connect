//go:build !windows && (!darwin || !arm64)

package proxy

import (
	"context"
	"errors"
)

type child struct{ mode Mode }

func (m *Manager) Start(raw string) error                                 { return m.StartMode(context.Background(), raw, ModeSystem) }
func (m *Manager) StartMode(_ context.Context, _ string, mode Mode) error { return m.CheckMode(mode) }
func (m *Manager) CheckMode(Mode) error                                   { return errors.New("代理模式仅支持 Windows") }
func (m *Manager) Refresh(context.Context, string) error {
	return nil
}
func (m *Manager) Stop() error    { return nil }
func (m *Manager) Recover() error { return nil }

func (m *Manager) Test(context.Context, string) error {
	return errors.New("代理连通性检测仅支持 Windows")
}
