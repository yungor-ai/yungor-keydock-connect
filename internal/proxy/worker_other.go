//go:build !darwin || !arm64

package proxy

func RunWorker([]string) (bool, error) { return false, nil }
