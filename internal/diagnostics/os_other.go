//go:build !windows

package diagnostics

func osVersion() string                 { return "" }
func platformNetworkError(error) string { return "" }
