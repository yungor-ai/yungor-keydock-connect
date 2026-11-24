package main

import (
	"encoding/hex"
	"net"
	"net/url"
)

func validWindowURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "/" || u.RawQuery != "" {
		return false
	}
	if _, _, err := net.SplitHostPort(u.Host); err != nil {
		return false
	}
	token, err := hex.DecodeString(u.Fragment)
	return err == nil && len(token) == 32
}
