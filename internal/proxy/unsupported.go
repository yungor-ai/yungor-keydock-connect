//go:build !windows

package proxy

import (
	"context"
	"errors"
)

type child struct{}

func (m *Manager) Start(string) error { return errors.New("系统代理仅支持 Windows") }
func (m *Manager) Stop() error        { return nil }
func (m *Manager) Recover() error     { return nil }

func (m *Manager) Test(context.Context, string) error {
	return errors.New("代理连通性检测仅支持 Windows")
}
