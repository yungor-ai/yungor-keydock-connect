//go:build darwin && arm64 && cgo

package main

/*
#cgo LDFLAGS: -framework ServiceManagement
const char *kdAuthorizeDarwinProxyService(void);
*/
import "C"

import "errors"

func authorizeDarwinProxyService() error {
	if message := C.kdAuthorizeDarwinProxyService(); message != nil {
		return errors.New(C.GoString(message))
	}
	return nil
}
