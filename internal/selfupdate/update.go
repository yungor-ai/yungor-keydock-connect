// Package selfupdate checks the chosen KeyDock server for desktop releases.
package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	keydockconnect "github.com/yungor-ai/yungor-keydock-connect"
	"github.com/yungor-ai/yungor-keydock-connect/internal/connect"
)

const (
	maxInstallerBytes = 200 << 20
	releasesByTag     = "https://api.github.com/repos/yungor-ai/yungor-keydock-connect/releases/tags/v"
	releaseAssets     = "https://github.com/yungor-ai/yungor-keydock-connect/releases/download/v"
)

var (
	stableVersion = regexp.MustCompile(`^(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})$`)
	updateHTTP    = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
)

type platform struct {
	id       string
	filename string
}

var platforms = map[string]platform{
	"windows/amd64": {"windows-x64", "KeyDockConnect-Setup-windows-x64.exe"},
	"darwin/arm64":  {"macos-arm64", "KeyDockConnect-macos-arm64.dmg"},
}

type Release struct {
	Version     string
	sha256      string
	downloadURL *url.URL
	filename    string
}

func SoftwareURL(server string) (string, error) {
	base, err := connect.ServerURL(strings.TrimSpace(server))
	if err != nil {
		return "", err
	}
	return base.JoinPath("software").String(), nil
}

// Check returns nil when the server has no newer installable release.
func Check(ctx context.Context, server string) (*Release, error) {
	return check(ctx, updateHTTP, server, keydockconnect.Version(), platforms[runtime.GOOS+"/"+runtime.GOARCH])
}

func check(ctx context.Context, client *http.Client, server, installed string, target platform) (*Release, error) {
	base, err := connect.ServerURL(strings.TrimSpace(server))
	if err != nil {
		return nil, err
	}
	current, valid := versionNumber(installed)
	if target.id == "" || !valid {
		return nil, errors.New("当前客户端版本不支持在线更新")
	}
	checkURL := base.JoinPath("api/connect/v1/update")
	checkURL.RawQuery = url.Values{"platform": {target.id}, "version": {installed}}.Encode()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	response, err := get(ctx, client, checkURL.String())
	if err != nil {
		return nil, errors.New("无法连接 KeyDock 检查更新，请检查服务地址和网络")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return nil, errors.New("该 KeyDock 服务暂不支持在线更新，请打开软件下载页面")
	case http.StatusServiceUnavailable:
		return nil, errors.New("KeyDock 暂无可用安装包，请稍后检查更新或打开软件下载页面")
	default:
		return nil, fmt.Errorf("KeyDock 更新检查失败（HTTP %d）", response.StatusCode)
	}

	var result struct {
		Available    *bool  `json:"update_available"`
		Version      string `json:"latest_version"`
		SHA256       string `json:"sha256"`
		DownloadPath string `json:"download_path"`
	}
	invalid := errors.New("KeyDock 返回的版本信息无效")
	if !decodeJSON(response.Body, &result, 8<<10) || result.Available == nil {
		return nil, invalid
	}
	latest, valid := versionNumber(result.Version)
	if !valid || *result.Available != (latest > current) {
		return nil, invalid
	}
	if !*result.Available {
		return nil, nil
	}
	if !validSHA256(result.SHA256) {
		return nil, invalid
	}
	downloadPath := "api/software/keydock-connect-" + target.id + "/download"
	query := url.Values{"digest": {result.SHA256}}.Encode()
	if result.DownloadPath != "/"+downloadPath+"?"+query {
		return nil, invalid
	}
	downloadURL := base.JoinPath(downloadPath)
	downloadURL.RawQuery = query
	return &Release{Version: result.Version, sha256: result.SHA256, downloadURL: downloadURL, filename: target.filename}, nil
}

// Verify rechecks the bytes of a downloaded installer immediately before use.
func (release *Release) Verify(installer io.Reader) bool {
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(installer, maxInstallerBytes+1))
	return err == nil && size > 0 && size <= maxInstallerBytes && hex.EncodeToString(hash.Sum(nil)) == release.sha256
}

// Download verifies the release against the publisher before fetching the
// installer from KeyDock. The caller owns the returned file's private directory.
func Download(ctx context.Context, release *Release) (string, error) {
	return download(ctx, updateHTTP, release)
}

func download(ctx context.Context, client *http.Client, release *Release) (string, error) {
	if release == nil || release.downloadURL == nil {
		return "", errors.New("请重新检查更新")
	}
	if _, valid := versionNumber(release.Version); !valid {
		return "", errors.New("请重新检查更新")
	}
	size, err := officialSize(ctx, client, release)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	response, err := get(ctx, client, release.downloadURL.String())
	if err != nil {
		return "", errors.New("无法下载更新，请检查与 KeyDock 的网络连接")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict || response.StatusCode == http.StatusServiceUnavailable {
		return "", errors.New("KeyDock 上的安装包已变化或暂不可用，请重新检查更新")
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载更新失败（HTTP %d）", response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != size {
		return "", errors.New("安装包大小与官方发布不符")
	}
	dir, err := os.MkdirTemp("", "keydock-update-")
	if err != nil {
		return "", errors.New("无法创建更新目录")
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(dir)
		}
	}()
	path := filepath.Join(dir, release.filename)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", errors.New("无法保存更新安装包")
	}
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, size+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return "", errors.New("安装包下载中断或保存失败，请重试")
	}
	if n != size || hex.EncodeToString(hash.Sum(nil)) != release.sha256 {
		return "", errors.New("安装包校验失败，请重新检查更新")
	}
	keep = true
	return path, nil
}

func officialSize(ctx context.Context, client *http.Client, release *Release) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	response, err := get(ctx, client, releasesByTag+release.Version)
	if err != nil {
		return 0, errors.New("无法连接 GitHub 核验官方发布，请检查网络后重试")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("无法核验 GitHub 官方发布（HTTP %d）", response.StatusCode)
	}
	var official struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
			State  string `json:"state"`
			Size   int64  `json:"size"`
		} `json:"assets"`
	}
	if decodeJSON(response.Body, &official, 1<<20) && official.Tag == "v"+release.Version && !official.Draft && !official.Prerelease {
		for _, asset := range official.Assets {
			if asset.Name == release.filename && asset.URL == releaseAssets+release.Version+"/"+release.filename && asset.State == "uploaded" &&
				asset.Digest == "sha256:"+release.sha256 && asset.Size > 0 && asset.Size <= maxInstallerBytes {
				return asset.Size, nil
			}
		}
	}
	return 0, errors.New("KeyDock 提供的安装包与 GitHub 官方发布不符")
}

func get(ctx context.Context, client *http.Client, address string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(request)
}

func decodeJSON(body io.Reader, output any, limit int64) bool {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	return err == nil && int64(len(data)) <= limit && json.Unmarshal(data, output) == nil
}

func validSHA256(digest string) bool {
	raw, err := hex.DecodeString(digest)
	return err == nil && len(raw) == sha256.Size && hex.EncodeToString(raw) == digest
}

func versionNumber(version string) (uint64, bool) {
	if !stableVersion.MatchString(version) {
		return 0, false
	}
	var number uint64
	for _, field := range strings.Split(version, ".") {
		value, _ := strconv.ParseUint(field, 10, 32)
		number = number*1_000_000 + value
	}
	return number, true
}
