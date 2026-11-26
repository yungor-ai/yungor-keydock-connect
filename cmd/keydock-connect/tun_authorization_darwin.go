//go:build darwin && arm64 && cgo

package main

/*
#cgo LDFLAGS: -framework ServiceManagement
const char *kdAuthorizeDarwinTUN(void);
*/
import "C"

import "errors"

func authorizeDarwinTUN() error {
	if message := C.kdAuthorizeDarwinTUN(); message != nil {
		return errors.New(C.GoString(message))
	}
	return nil
}
