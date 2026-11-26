//go:build !windows && (!darwin || !arm64 || !cgo)

package codex

import "errors"

var unsupported = errors.New("凭据存储仅支持 Windows x64 与 macOS Apple Silicon 桌面客户端")

func ProtectForState(string, []byte) ([]byte, error)   { return nil, unsupported }
func UnprotectForState(string, []byte) ([]byte, error) { return nil, unsupported }
