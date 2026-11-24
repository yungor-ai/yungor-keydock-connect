// Package connect talks only to the employee's chosen KeyDock and model endpoints.
package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Profile holds the personal materials assigned by the server. Its JSON field names
// must also remain compatible with the v0.1 Windows DPAPI device record.
type Profile struct{ DeviceName, BaseURL, Key, Model, SubscriptionURL string }

type Client struct{ HTTP *http.Client }

func valid(value string, max int) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= max && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}
func token(key string) bool {
	if !valid(key, 4096) {
		return false
	}
	for _, r := range key {
		if r < '!' || r > '~' {
			return false
		}
	}
	return true
}
func endpoint(raw string, allowQuery bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if !valid(raw, 8192) || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		strings.ContainsAny(raw, "#\\") || (!allowQuery && (u.RawQuery != "" || u.ForceQuery)) {
		return nil, errors.New("请输入有效的 HTTPS 服务地址")
	}
	return u, nil
}

type responseStatus int

func (s responseStatus) Error() string {
	if s == http.StatusUnauthorized || s == http.StatusForbidden {
		return fmt.Sprintf("服务拒绝了当前密钥（HTTP %d）", s)
	}
	return fmt.Sprintf("服务返回 HTTP %d", s)
}

// Copy the supplied client so redirect policy cannot transfer a credential to
// another origin or resend it at a different path on the same origin.
func (c Client) post(ctx context.Context, destination *url.URL, key string, body []byte) (*http.Response, error) {
	client := http.Client{}
	if c.HTTP != nil {
		client = *c.HTTP
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, destination.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("无法构建服务请求")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("无法连接服务，请检查服务地址、网络和 TLS 证书")
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, responseStatus(resp.StatusCode)
	}
	return resp, nil
}
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || int64(len(data)) > limit || !utf8.Valid(data) {
		return nil, errors.New("服务响应无效或超出大小限制")
	}
	return data, nil
}

func (c Client) Fetch(ctx context.Context, serverURL, deviceKey string) (Profile, error) {
	var p Profile
	u, err := endpoint(serverURL, false)
	if err != nil {
		return p, err
	}
	if !token(deviceKey) {
		return p, errors.New("设备密钥格式无效")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	resp, err := c.post(ctx, u.JoinPath("api/connect/v1/device"), deviceKey, []byte("{}"))
	if err != nil {
		switch err {
		case responseStatus(http.StatusNotFound):
			return p, errors.New("未找到设备接入接口（HTTP 404），请核对 KeyDock 地址和服务端版本")
		case responseStatus(http.StatusConflict):
			return p, errors.New("设备配置尚未就绪（HTTP 409），请管理员核对默认模型、个人 Key 和 SS 订阅")
		case responseStatus(http.StatusTooManyRequests):
			return p, errors.New("设备连接过于频繁（HTTP 429），请稍后重试")
		}
		return p, err
	}
	defer resp.Body.Close()
	data, err := readLimited(resp.Body, 64<<10)
	if err != nil {
		return p, err
	}
	var d struct {
		Schema       int             `json:"schema_version"`
		Device       string          `json:"device_name"`
		Base         string          `json:"api_base_url"`
		Key          string          `json:"api_key"`
		Model        string          `json:"model"`
		Subscription json.RawMessage `json:"subscription_url"`
	}
	invalid := errors.New("KeyDock 返回的设备配置不完整或格式不受支持")
	if json.Unmarshal(data, &d) != nil || d.Schema != 1 || !valid(d.Device, 1024) || !token(d.Key) || d.Key == deviceKey || !valid(d.Model, 200) || len(d.Subscription) == 0 {
		return p, invalid
	}
	if _, err = endpoint(d.Base, false); err != nil {
		return p, invalid
	}
	var sub string
	// An explicitly empty or null subscription means this device has model access only.
	if json.Unmarshal(d.Subscription, &sub) != nil {
		return p, invalid
	}
	if sub != "" {
		if _, err = endpoint(sub, true); err != nil {
			return p, invalid
		}
	}
	return Profile{DeviceName: d.Device, BaseURL: d.Base, Key: d.Key, Model: d.Model, SubscriptionURL: sub}, nil
}
