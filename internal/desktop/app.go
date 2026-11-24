// Package desktop serves the local, single-user KeyDock Connect window.
package desktop

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yungor-ai/yungor-keydock-connect/internal/bundle"
	"github.com/yungor-ai/yungor-keydock-connect/internal/codex"
)

type networkProxy interface {
	Start(string) error
	Stop() error
	Active() bool
}
type Backend struct {
	Apply              func(string, string, string, string, string) error
	Restore            func(string, string) error
	Configured         func(string, string) bool
	Matches            func(string, string, string, string, string) bool
	Protect, Unprotect func([]byte) ([]byte, error)
	Launch             func() error
}

func SystemBackend() Backend {
	return Backend{Apply: codex.Apply, Restore: codex.Restore, Configured: codex.Configured, Matches: codex.Matches, Protect: codex.Protect, Unprotect: codex.Unprotect, Launch: launchCodex}
}

type App struct {
	mu                          sync.Mutex
	host, token, home, stateDir string
	proxy                       networkProxy
	ops                         Backend
	profile                     bundle.Profile
	quit                        func()
	closing                     bool
}

func New(host, home, stateDir string, proxy networkProxy, ops Backend, quit func()) (*App, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	a := &App{host: host, token: hex.EncodeToString(token), home: home, stateDir: stateDir, proxy: proxy, ops: ops, quit: quit}
	raw, err := os.ReadFile(a.profilePath())
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := ops.Unprotect(raw)
	if err != nil {
		return nil, errors.New("无法解密已保存的设备资料，请使用原 Windows 账户打开")
	}
	if json.Unmarshal(plain, &a.profile) != nil || a.profile.Key == "" {
		return nil, errors.New("已保存的设备资料损坏")
	}
	return a, nil
}
func (a *App) URL() string         { return "http://" + a.host + "/#" + a.token }
func (a *App) profilePath() string { return filepath.Join(a.stateDir, "device.bin") }
func (a *App) save(p bundle.Profile) error {
	plain, err := json.Marshal(p)
	if err != nil {
		return err
	}
	encrypted, err := a.ops.Protect(plain)
	if err != nil {
		return errors.New("设备资料加密失败")
	}
	if err = WritePrivate(a.profilePath(), encrypted); err != nil {
		return errors.New("设备资料保存失败")
	}
	a.profile = p
	return nil
}
func WritePrivate(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".keydock-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	return os.Rename(f.Name(), path)
}

type status struct {
	Device       string `json:"device"`
	Base         string `json:"base"`
	Model        string `json:"model"`
	Imported     bool   `json:"imported"`
	Configured   bool   `json:"configured"`
	Restorable   bool   `json:"restorable"`
	Proxy        bool   `json:"proxy"`
	Subscription bool   `json:"subscription"`
}

func (a *App) status() status {
	p := a.profile
	return status{Device: p.DeviceName, Base: p.BaseURL, Model: p.Model, Imported: p.Key != "", Configured: p.Key != "" && a.ops.Matches(a.home, a.stateDir, p.BaseURL, p.Model, p.Key), Restorable: a.ops.Configured(a.home, a.stateDir), Proxy: a.proxy.Active(), Subscription: p.SubscriptionURL != ""}
}
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	if r.Host != a.host {
		http.Error(w, "invalid host", http.StatusForbidden)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		a.servePage(w, r)
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		fail(w, 405, "请求方法错误")
		return
	}
	if r.Header.Get("Origin") != "http://"+a.host || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-KeyDock-Session")), []byte(a.token)) != 1 {
		fail(w, 403, "请从 KeyDock 接入助手重新打开窗口")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		fail(w, 409, "程序正在退出")
		return
	}
	var err error
	switch r.URL.Path {
	case "/api/state":
	case "/api/import":
		err = a.importBundle(w, r)
	case "/api/models":
		if a.profile.Key == "" {
			err = errors.New("请先导入设备包")
			break
		}
		var models []string
		models, err = availableModels(r.Context(), a.profile)
		if err == nil {
			writeJSON(w, 200, map[string]any{"models": models})
			return
		}
	case "/api/apply":
		var arg struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&arg) != nil {
			err = errors.New("模型名称无效")
			break
		}
		model := strings.TrimSpace(arg.Model)
		p := a.profile
		if p.Key == "" || model == "" {
			err = errors.New("请先导入设备包并选择模型")
			break
		}
		if err = a.ops.Apply(a.home, a.stateDir, p.BaseURL, model, p.Key); err == nil && model != p.Model {
			p.Model = model
			if err = a.save(p); err != nil {
				err = errors.New("Codex 配置已应用，但未能更新本地设备资料，请重试以同步所选模型")
			}
		}
	case "/api/launch":
		err = a.ops.Launch()
	case "/api/restore":
		err = a.ops.Restore(a.home, a.stateDir)
	case "/api/proxy/on":
		if a.profile.SubscriptionURL == "" {
			err = errors.New("该设备没有可用的代理订阅")
			break
		}
		err = a.proxy.Start(a.profile.SubscriptionURL)
	case "/api/proxy/off":
		err = a.proxy.Stop()
	case "/api/quit":
		if err = a.proxy.Stop(); err == nil {
			a.closing = true
			defer a.quit()
		}
	default:
		fail(w, 404, "没有此操作")
		return
	}
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, a.status())
}

const maxBundle = bundle.MaxArchiveBytes

func (a *App) importBundle(w http.ResponseWriter, r *http.Request) error {
	if a.proxy.Active() {
		return errors.New("请先关闭系统代理，再更换设备包")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBundle+64*1024)
	m, err := r.MultipartReader()
	if err != nil {
		return errors.New("请同时选择接入包并输入口令")
	}
	var data []byte
	var password string
	for {
		part, err := m.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("无法读取接入包")
		}
		switch part.FormName() {
		case "package":
			if data != nil {
				return errors.New("请只选择一个设备包")
			}
			data, err = readPart(part, maxBundle)
		case "password":
			var v []byte
			v, err = readPart(part, 1024)
			password = string(v)
		default:
			return errors.New("导入表单不正确")
		}
		if err != nil {
			return err
		}
	}
	if len(data) == 0 || password == "" {
		return errors.New("请选择接入包并输入口令")
	}
	p, err := bundle.Read(data, password)
	if err != nil {
		return err
	}
	p.BaseURL = migratedBaseURL(p.BaseURL)
	if _, err = gatewayURL(p.BaseURL); err != nil {
		return err
	}
	return a.save(p)
}
func readPart(p *multipart.Part, max int64) ([]byte, error) {
	defer p.Close()
	b, err := io.ReadAll(io.LimitReader(p, max+1))
	if err != nil || int64(len(b)) > max {
		return nil, errors.New("文件或口令超出大小限制")
	}
	return b, nil
}
func gatewayURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("接入包需要使用 HTTPS 模型网关，请向管理员获取更新的接入包")
	}
	return u, nil
}
func availableModels(ctx context.Context, p bundle.Profile) ([]string, error) {
	u, err := gatewayURL(p.BaseURL)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/models"
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, errors.New("模型地址无效")
	}
	req.Header.Set("Authorization", "Bearer "+p.Key)
	client := &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("无法连接模型网关，请检查网络")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("模型网关返回 HTTP %d", res.StatusCode)
	}
	if t, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type")); t != "application/json" {
		return nil, errors.New("模型网关未返回 JSON")
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&list); err != nil {
		return nil, errors.New("模型列表格式错误")
	}
	var result []string
	for _, v := range list.Data {
		if len(v.ID) > 0 && len(v.ID) <= 200 {
			result = append(result, v.ID)
		}
	}
	return result, nil
}

// The legacy KeyDock IP ingress exposes the same service at /v1 on port 443.
// Never guess an HTTPS host for a different operator or send the bearer over HTTP.
func migratedBaseURL(raw string) string {
	switch raw {
	case "http://144.34.153.220/v1", "http://144.34.153.220/v1/", "http://144.34.153.220/cliproxy/v1", "http://144.34.153.220/cliproxy/v1/":
		return "https://144.34.153.220/v1"
	}
	return raw
}
