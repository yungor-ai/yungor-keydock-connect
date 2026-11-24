// Package codex configures a Codex gateway while preserving the user's existing settings.
package codex

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

const providerID = "keydock-connect"

func validInputs(base, model, key string) error {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.ForceQuery || strings.ContainsAny(base, "?#") {
		return errors.New("模型网关必须是无凭据、查询参数和片段的 HTTPS 地址")
	}
	for _, value := range []string{model, key} {
		if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return errors.New("模型或 API Key 为空或包含无效字符")
		}
	}
	return nil
}

// JSON strings containing normal path/label characters are also TOML basic strings.
func quoted(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

type edit struct {
	start, end int
	value      string
}

// Build from the original bytes on each apply so our own table never accumulates.
// Edit root string literals only; all existing comments and unrelated settings survive.
func render(original []byte, base, model, executable string) ([]byte, error) {
	if !filepath.IsAbs(executable) {
		return nil, errors.New("凭据助手路径必须为绝对路径")
	}
	bom := []byte{0xef, 0xbb, 0xbf}
	body := bytes.TrimPrefix(original, bom)
	var existing map[string]any
	if err := toml.Unmarshal(body, &existing); err != nil {
		return nil, errors.New("现有 Codex 配置不是有效的 TOML")
	}
	if value, found := existing["model_providers"]; found {
		providers, ok := value.(map[string]any)
		if !ok {
			return nil, errors.New("现有 model_providers 配置类型无效")
		}
		if _, found := providers[providerID]; found {
			return nil, errors.New("现有配置已使用 keydock-connect 提供商名称")
		}
	}
	target := map[string]string{"model": model, "model_provider": providerID}
	var p unstable.Parser
	p.Reset(body)
	var edits []edit
	inlineProvidersAt := -1
	for p.NextExpression() {
		n := p.Expression()
		if n.Kind == unstable.Table || n.Kind == unstable.ArrayTable {
			break // following key-values belong to a table
		}
		if n.Kind != unstable.KeyValue {
			continue
		}
		names := n.Key()
		names.Next()
		name := string(names.Node().Data)
		if names.Next() {
			continue
		}
		if name == "model_providers" && n.Value().Kind == unstable.InlineTable {
			inlineProvidersAt = int(n.Raw.Offset+n.Raw.Length) - 1
		}
		if value, present := target[name]; present {
			if n.Value().Kind != unstable.String {
				return nil, errors.New("现有 model 或 model_provider 配置类型无效")
			}
			r := n.Value().Raw
			edits = append(edits, edit{int(r.Offset), int(r.Offset + r.Length), quoted(value)})
			delete(target, name)
		}
	}
	newline := "\n"
	if bytes.Contains(body, []byte("\r\n")) {
		newline = "\r\n"
	}
	var prefix strings.Builder
	for _, name := range []string{"model", "model_provider"} {
		if value, present := target[name]; present {
			prefix.WriteString(name + " = " + quoted(value) + newline)
		}
	}
	pairs := []string{"name = \"KeyDock\"", "base_url = " + quoted(base), "wire_api = \"responses\""}
	auth := []string{"command = " + quoted(executable), "args = [\"--credential\"]"}
	var suffix string
	if inlineProvidersAt >= 0 {
		// An inline table is sealed in TOML and cannot be extended using a table header.
		separator := ""
		if len(existing["model_providers"].(map[string]any)) > 0 {
			if bytes.HasSuffix(bytes.TrimSpace(body[:inlineProvidersAt]), []byte(",")) {
				separator = " "
			} else {
				separator = ", "
			}
		}
		fields := append(pairs, "auth = { "+strings.Join(auth, ", ")+" }")
		edits = append(edits, edit{inlineProvidersAt, inlineProvidersAt, separator + providerID + " = { " + strings.Join(fields, ", ") + " }"})
	} else {
		header := "model_providers." + providerID
		suffix = newline + "[" + header + "]" + newline + strings.Join(pairs, newline) + newline + "[" + header + ".auth]" + newline + strings.Join(auth, newline) + newline
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var out bytes.Buffer
	if len(body) != len(original) {
		out.Write(bom)
	}
	out.WriteString(prefix.String())
	last := 0
	for _, e := range edits {
		out.Write(body[last:e.start])
		out.WriteString(e.value)
		last = e.end
	}
	out.Write(body[last:])
	out.WriteString(suffix)
	if err := toml.Unmarshal(bytes.TrimPrefix(out.Bytes(), bom), &map[string]any{}); err != nil {
		return nil, errors.New("无法安全合并此 Codex 配置")
	}
	return out.Bytes(), nil
}
