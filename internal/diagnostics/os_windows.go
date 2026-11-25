package diagnostics

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

func osVersion() string {
	v := windows.RtlGetVersion()
	return fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
}

func platformNetworkError(err error) string {
	switch {
	case errors.Is(err, windows.WSAECONNREFUSED):
		return "connection_refused"
	case errors.Is(err, windows.WSAECONNRESET), errors.Is(err, windows.WSAECONNABORTED):
		return "connection_reset"
	case errors.Is(err, windows.WSAENETUNREACH), errors.Is(err, windows.WSAEHOSTUNREACH):
		return "network_unreachable"
	case errors.Is(err, windows.WSAETIMEDOUT):
		return "timeout"
	}
	return ""
}
