//go:build darwin && arm64 && !cgo

package proxy

import (
	"errors"
	"net"
)

func runDarwinTUNService() error { return errors.New("TUN 后台服务需要原生 macOS 构建") }

func requestDarwinTUNChannel() (*net.UnixConn, error) { return nil, runDarwinTUNService() }
