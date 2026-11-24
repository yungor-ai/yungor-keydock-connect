// Package bundle imports the encrypted personal ZIP produced by KeyDock.
package bundle

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	zip "go.foxforensics.eu/go-zip/zip"
)

type Profile struct {
	DeviceName, BaseURL, Key, Model, SubscriptionURL string
}

const (
	// KeyDock permits up to 200 MiB of installers in its optional outer ZIP.
	MaxArchiveBytes = 220 << 20
	innerLimit      = 8 << 20
	valueLimit      = 128 << 10
	innerName       = "个人接入包.zip"
	manifestName    = "配置清单.json"
	accountName     = "TokenHub/账号与入口.json"
	legacyName      = "TokenHub/个人接入信息.json"
	modelName       = "Codex/config.toml"
)

var (
	errFormat = errors.New("个人接入包无效、超出限制或格式不受支持")
	errRead   = errors.New("无法读取个人接入包，请检查口令和文件完整性")
)

type archive map[string]*zip.File

// Read never extracts files. Only the authenticated personal fields chosen for
// this device are consumed. Installers, scripts and unrelated keys stay closed.
func Read(data []byte, password string) (Profile, error) {
	if len(data) > MaxArchiveBytes || password == "" || len(password) > 1024 {
		return Profile{}, errFormat
	}
	files, err := index(data)
	if err != nil {
		return Profile{}, err
	}
	if inner := files[innerName]; inner != nil {
		if inner.IsEncrypted() || files.hasPrivateFiles() {
			return Profile{}, errFormat
		}
		data, err = readEntry(inner, innerLimit)
		if err != nil {
			return Profile{}, err
		}
		files, err = index(data)
		if err != nil {
			return Profile{}, err
		}
	}
	if len(data) > innerLimit || files[innerName] != nil {
		return Profile{}, errFormat
	}
	for _, f := range files {
		if !f.IsEncrypted() || !aes256(f.Extra) {
			return Profile{}, errFormat
		}
		f.SetPassword(password)
	}
	p, err := files.credentials()
	if err != nil {
		return Profile{}, err
	}
	if f := files[modelName]; f != nil {
		data, err := readEntry(f, valueLimit)
		if err != nil {
			return Profile{}, err
		}
		var config struct {
			Model string `toml:"model"`
		}
		if toml.Unmarshal(data, &config) != nil || !valid(config.Model, 200) {
			return Profile{}, errFormat
		}
		p.Model = config.Model
	}
	if !valid(p.BaseURL, 8192) || !valid(p.Key, 4096) || !valid(p.SubscriptionURL, 8192) || !valid(p.DeviceName, 1024) {
		return Profile{}, errFormat
	}
	return p, nil
}

// The public wrapper may contain installers, licenses and guides. Device data
// in the outer archive is never accepted, even if an encrypted inner ZIP exists.
func (files archive) hasPrivateFiles() bool {
	for name := range files {
		n := strings.ToLower(name)
		if n == manifestName || strings.HasPrefix(n, "tokenhub/") || strings.HasPrefix(n, "codex/") || strings.HasPrefix(n, "clash/") {
			return true
		}
	}
	return false
}

func valid(value string, maxBytes int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxBytes && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

// Both ZIPs emitted by KeyDock have a classic, comment-free end record.
// Bound the directory itself before the ZIP library allocates per-entry state.
func index(data []byte) (archive, error) {
	if len(data) < 22 {
		return nil, errFormat
	}
	end := data[len(data)-22:]
	order := binary.LittleEndian
	count := int(order.Uint16(end[10:12]))
	size := uint64(order.Uint32(end[12:16]))
	offset := uint64(order.Uint32(end[16:20]))
	if !bytes.Equal(end[:4], []byte("PK\x05\x06")) || order.Uint32(end[4:8]) != 0 ||
		order.Uint16(end[20:]) != 0 || count < 1 || count > 512 || int(order.Uint16(end[8:10])) != count ||
		size > 128<<10 || offset+size != uint64(len(data)-22) {
		return nil, errFormat
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(reader.File) != count {
		return nil, errFormat
	}
	files := make(archive, count)
	seen := make(map[string]bool, count)
	for _, f := range reader.File {
		name := strings.TrimSuffix(f.Name, "/")
		folded := strings.ToLower(name)
		if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:\x00") || seen[folded] {
			return nil, errFormat
		}
		seen[folded] = true
		if !strings.HasSuffix(f.Name, "/") {
			files[f.Name] = f
		}
	}
	return files, nil
}

// KeyDock writer uses WinZip AE-2, strength 3 (AES-256). The flag alone
// also identifies legacy ZipCrypto, which provides no authenticated reads.
func aes256(extra []byte) bool {
	found := false
	for len(extra) > 0 {
		if len(extra) < 4 {
			return false
		}
		tag := binary.LittleEndian.Uint16(extra)
		n := int(binary.LittleEndian.Uint16(extra[2:]))
		extra = extra[4:]
		if n > len(extra) {
			return false
		}
		if tag == 0x9901 {
			if found || n != 7 || !bytes.Equal(extra[:5], []byte{2, 0, 'A', 'E', 3}) {
				return false
			}
			found = true
		}
		extra = extra[n:]
	}
	return found
}

func readEntry(f *zip.File, limit int) ([]byte, error) {
	if f == nil || !f.Mode().IsRegular() || f.UncompressedSize64 > uint64(limit) ||
		f.CompressedSize64 > uint64(limit+4096) || (f.IsEncrypted() && f.CompressedSize64 < 28) {
		return nil, errFormat
	}
	r, err := f.Open()
	if err != nil {
		return nil, errRead
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil || len(data) > limit || uint64(len(data)) != f.UncompressedSize64 {
		return nil, errRead
	}
	return data, nil
}

func (files archive) readJSON(name string, dest any) error {
	data, err := readEntry(files[name], valueLimit)
	if err != nil {
		return err
	}
	if !utf8.Valid(data) || json.Unmarshal(data, dest) != nil {
		return errFormat
	}
	return nil
}

type account struct {
	Schema          int    `json:"schema_version"`
	DeviceID        int64  `json:"device_id"`
	DeviceName      string `json:"device_name"`
	BaseURL         string `json:"api_base_url"`
	Key             string `json:"api_key"`
	SubscriptionURL string `json:"clash_subscription_url"`
}

type keyReference struct {
	ID       int64  `json:"id"`
	DeviceID int64  `json:"device_id"`
	Path     string `json:"path"`
}

func (files archive) credentials() (Profile, error) {
	var a account
	if files[legacyName] != nil {
		if files[manifestName] != nil || files[accountName] != nil {
			return Profile{}, errFormat
		}
		if err := files.readJSON(legacyName, &a); err != nil {
			return Profile{}, err
		}
		if a.Schema != 0 && a.Schema != 1 {
			return Profile{}, errFormat
		}
	} else {
		if err := files.readJSON(accountName, &a); err != nil {
			return Profile{}, err
		}
		if a.Schema != 2 || a.DeviceID < 1 || a.Key != "" {
			return Profile{}, errFormat
		}
		key, err := files.defaultKey(a.DeviceID)
		if err != nil {
			return Profile{}, err
		}
		a.Key = key
	}
	return Profile{DeviceName: a.DeviceName, BaseURL: a.BaseURL, Key: a.Key, SubscriptionURL: a.SubscriptionURL}, nil
}

func (files archive) defaultKey(deviceID int64) (string, error) {
	var manifest struct {
		Schema          int            `json:"schema_version"`
		DeliveryVersion int64          `json:"delivery_version"`
		DeviceID        int64          `json:"device_id"`
		DefaultID       int64          `json:"codex_default_api_key_id"`
		Keys            []keyReference `json:"api_keys"`
	}
	if err := files.readJSON(manifestName, &manifest); err != nil {
		return "", err
	}
	if manifest.Schema != 2 || manifest.DeviceID != deviceID || manifest.DeliveryVersion < 1 || manifest.DefaultID < 1 ||
		len(manifest.Keys) == 0 || len(manifest.Keys) > 100 {
		return "", errFormat
	}
	seen := make(map[int64]bool, len(manifest.Keys))
	var selected string
	for _, ref := range manifest.Keys {
		path := "TokenHub/keys/" + strconv.FormatInt(ref.ID, 10) + ".json"
		if ref.ID < 1 || seen[ref.ID] || ref.DeviceID != deviceID || ref.Path != path || files[path] == nil {
			return "", errFormat
		}
		seen[ref.ID] = true
		if ref.ID == manifest.DefaultID {
			selected = path
		}
	}
	var material struct {
		Schema   int    `json:"schema_version"`
		ID       int64  `json:"id"`
		DeviceID int64  `json:"device_id"`
		Key      string `json:"api_key"`
	}
	if err := files.readJSON(selected, &material); err != nil {
		return "", err
	}
	if material.Schema != 2 || material.ID != manifest.DefaultID || material.DeviceID != deviceID {
		return "", errFormat
	}
	return material.Key, nil
}
