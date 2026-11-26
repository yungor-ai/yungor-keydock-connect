//go:build !windows && !darwin

package diagnostics

func osVersion() string                 { return "" }
func platformNetworkError(error) string { return "" }
