package connect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"strings"
	"time"
)

const modelLimit = 1 << 20

var errNoCompletion = errors.New("模型服务未返回完整的文本回复")

type modelResponse struct {
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error"`
	Output []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
}

func (r modelResponse) completed() bool {
	return r.Status == "completed" && (len(r.Error) == 0 || bytes.Equal(r.Error, []byte("null")))
}
func (r modelResponse) hasText() bool {
	for _, item := range r.Output {
		if item.Type != "message" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" && strings.TrimSpace(part.Text) != "" {
				return true
			}
		}
	}
	return false
}
func (c Client) TestModel(ctx context.Context, p Profile) error {
	u, err := endpoint(p.BaseURL, false)
	if err != nil {
		return err
	}
	if !token(p.Key) || !valid(p.Model, 200) {
		return errors.New("设备模型或 API 密钥格式无效")
	}
	payload, _ := json.Marshal(map[string]any{
		"model":        p.Model,
		"instructions": "Return only OK.",
		"input": []map[string]any{{
			"type":    "message",
			"role":    "user",
			"content": []map[string]string{{"type": "input_text", "text": "Reply OK."}},
		}},
		"max_output_tokens": 64,
		"store":             false,
		"stream":            true,
	})
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	resp, err := c.post(ctx, u.JoinPath("responses"), p.Key, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if media == "text/event-stream" {
		return completedStream(resp.Body)
	}
	data, err := readLimited(resp.Body, modelLimit)
	if err != nil {
		return err
	}
	var r modelResponse
	if json.Unmarshal(data, &r) != nil || !r.completed() || !r.hasText() {
		return errNoCompletion
	}
	return nil
}

func completedStream(body io.Reader) error {
	limited := &io.LimitedReader{R: body, N: modelLimit + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), modelLimit)
	var data bytes.Buffer
	var hasText bool
	for scanner.Scan() {
		line := scanner.Bytes()
		if bytes.HasPrefix(line, []byte("data:")) {
			fragment := bytes.TrimPrefix(line, []byte("data:"))
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(bytes.TrimPrefix(fragment, []byte(" ")))
		}
		if len(line) != 0 || data.Len() == 0 {
			continue
		}
		var event struct {
			Type     string        `json:"type"`
			Delta    string        `json:"delta"`
			Text     string        `json:"text"`
			Response modelResponse `json:"response"`
		}
		if json.Unmarshal(data.Bytes(), &event) != nil {
			return errNoCompletion
		}
		data.Reset()
		switch event.Type {
		case "response.output_text.delta":
			hasText = hasText || strings.TrimSpace(event.Delta) != ""
		case "response.output_text.done":
			hasText = hasText || strings.TrimSpace(event.Text) != ""
		case "response.failed", "response.incomplete", "error":
			return errNoCompletion
		case "response.completed":
			if limited.N == 0 || !event.Response.completed() || (!event.Response.hasText() && !hasText) {
				return errNoCompletion
			}
			return nil
		}
	}
	return errNoCompletion
}
