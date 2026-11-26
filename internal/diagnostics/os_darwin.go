package diagnostics

import "golang.org/x/sys/unix"

func osVersion() string {
	version, _ := unix.Sysctl("kern.osproductversion")
	return version
}

func platformNetworkError(error) string { return "" }
