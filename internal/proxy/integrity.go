package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

var geoFiles = map[string]string{
	"GeoSite.dat":  "2bdf171e1862b47cc5cd322ee43e5d066d9963025919ebb58dc7891d2b92643e",
	"geoip.metadb": "4236cf8477d12ceb4837eff39a03a65d5d55a72d3cccdc92e2e36d9cdbd686e3",
}

func digestCopy(path string, out io.Writer, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("缺少内置代理核心或离线分流数据，请重新安装")
	}
	defer f.Close()
	return verifyDigest(f, out, expected)
}

func verifyDigest(source io.Reader, out io.Writer, expected string) error {
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), source); err != nil || hex.EncodeToString(h.Sum(nil)) != expected {
		return errors.New("代理组件校验失败，请重新安装")
	}
	return nil
}
