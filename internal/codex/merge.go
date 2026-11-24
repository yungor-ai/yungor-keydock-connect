package codex

import (
	"bytes"
	"reflect"
	"sort"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

func parseSettings(data []byte) (map[string]any, error) {
	doc := map[string]any{}
	err := toml.Unmarshal(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}), &doc)
	return doc, err
}

func providerValues(doc map[string]any) any {
	providers, _ := doc["model_providers"].(map[string]any)
	return providers[providerID]
}

func keys(n *unstable.Node) []string {
	var result []string
	it := n.Key()
	for it.Next() {
		result = append(result, string(it.Node().Data))
	}
	return result
}

func rootLiterals(doc []byte) map[string]string {
	result := map[string]string{}
	var p unstable.Parser
	p.Reset(bytes.TrimPrefix(doc, []byte{0xef, 0xbb, 0xbf}))
	for p.NextExpression() {
		n := p.Expression()
		if n.Kind == unstable.Table || n.Kind == unstable.ArrayTable {
			break
		}
		if n.Kind != unstable.KeyValue {
			continue
		}
		names := keys(n)
		if len(names) == 1 && (names[0] == "model" || names[0] == "model_provider") {
			result[names[0]] = string(p.Raw(n.Value().Raw))
		}
	}
	return result
}

// Remove the expression and its line ending if the rest of the line is blank.
// A trailing comment is retained as its own comment.
func removeExpression(data []byte, start, end int) edit {
	lineStart := bytes.LastIndexByte(data[:start], '\n') + 1
	if len(bytes.TrimSpace(data[lineStart:start])) == 0 {
		start = lineStart
	}
	offset := bytes.IndexByte(data[end:], '\n')
	lineEnd := len(data)
	if offset >= 0 {
		lineEnd = end + offset + 1
	}
	if len(bytes.TrimSpace(data[end:lineEnd])) == 0 {
		end = lineEnd
	}
	return edit{start: start, end: end}
}

func removeHeader(body []byte, n *unstable.Node) edit {
	it := n.Key()
	it.Next()
	first := int(it.Node().Raw.Offset)
	last := it.Node().Raw
	for it.Next() {
		last = it.Node().Raw
	}
	start := bytes.LastIndexByte(body[:first], '\n') + 1
	end := int(last.Offset + last.Length)
	for end < len(body) && (body[end] == ' ' || body[end] == '\t' || body[end] == ']') {
		end++
	}
	return removeExpression(body, start, end)
}

func inlineProviderEdit(body []byte, n *unstable.Node) (edit, bool) {
	it := n.Children()
	for it.Next() {
		item := it.Node()
		if item.Kind != unstable.KeyValue {
			continue
		}
		k := keys(item)
		if len(k) != 1 || k[0] != providerID {
			continue
		}
		start, end := int(item.Raw.Offset), int(item.Raw.Offset+item.Raw.Length)
		// Prefer removing the immediately preceding separator so an existing
		// trailing comma and neighbouring entries retain their spelling.
		before := start
		for before > 0 && (body[before-1] == ' ' || body[before-1] == '\t' || body[before-1] == '\r' || body[before-1] == '\n') {
			before--
		}
		if before > 0 && body[before-1] == ',' {
			return edit{start: before - 1, end: end}, true
		}
		after := end
		for after < len(body) && (body[after] == ' ' || body[after] == '\t' || body[after] == '\r' || body[after] == '\n') {
			after++
		}
		if after < len(body) && body[after] == ',' {
			end = after + 1
		}
		return edit{start: start, end: end}, true
	}
	return edit{}, false
}

// Three-way undo: require the live managed values to match our last installation.
// Remove just those expressions and put back the previous selection. Keeping the
// live source bytes outside the edits preserves comments and newly trusted projects.
func (s *state) unconfigured(current snapshot) (snapshot, error) {
	if current.digest() == s.expected() {
		return s.Original, nil
	}
	if s.Active == nil || !current.Present {
		return snapshot{}, conflict
	}
	installed, err := render(s.Original.Data, s.Active.BaseURL, s.Active.Model, s.Active.Command)
	if err != nil {
		return snapshot{}, conflict
	}
	ours, err := parseSettings(installed)
	if err != nil {
		return snapshot{}, conflict
	}
	live, err := parseSettings(current.Data)
	if err != nil || live["model"] != ours["model"] || live["model_provider"] != ours["model_provider"] || !reflect.DeepEqual(providerValues(live), providerValues(ours)) {
		return snapshot{}, conflict
	}
	original, err := parseSettings(s.Original.Data)
	if err != nil {
		return snapshot{}, conflict
	}
	originalValues := rootLiterals(s.Original.Data)
	bom := []byte{0xef, 0xbb, 0xbf}
	body := bytes.TrimPrefix(current.Data, bom)
	var edits []edit
	var p unstable.Parser
	p.Reset(body)
	var table []string
	for p.NextExpression() {
		n := p.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			table = keys(n)
			if len(table) >= 2 && table[0] == "model_providers" && table[1] == providerID {
				edits = append(edits, removeHeader(body, n))
			}
		case unstable.KeyValue:
			path := append(append([]string(nil), table...), keys(n)...)
			expr := n.Raw
			if len(path) == 1 && (path[0] == "model" || path[0] == "model_provider") {
				if raw, ok := originalValues[path[0]]; ok {
					r := n.Value().Raw
					edits = append(edits, edit{int(r.Offset), int(r.Offset + r.Length), raw})
				} else {
					edits = append(edits, removeExpression(body, int(expr.Offset), int(expr.Offset+expr.Length)))
				}
			} else if len(path) >= 2 && path[0] == "model_providers" && path[1] == providerID {
				edits = append(edits, removeExpression(body, int(expr.Offset), int(expr.Offset+expr.Length)))
			} else if len(path) == 1 && path[0] == "model_providers" && n.Value().Kind == unstable.InlineTable {
				e, ok := inlineProviderEdit(body, n.Value())
				if !ok {
					return snapshot{}, conflict
				}
				edits = append(edits, e)
			}
		}
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var output bytes.Buffer
	if len(body) != len(current.Data) {
		output.Write(bom)
	}
	last := 0
	for _, e := range edits {
		if e.start < last || e.end > len(body) {
			return snapshot{}, conflict
		}
		output.Write(body[last:e.start])
		output.WriteString(e.value)
		last = e.end
	}
	output.Write(body[last:])
	result := output.Bytes()
	// Check every unchanged setting semantically before writing a merged config.
	for _, name := range []string{"model", "model_provider"} {
		if v, ok := original[name]; ok {
			live[name] = v
		} else {
			delete(live, name)
		}
	}
	providers, _ := live["model_providers"].(map[string]any)
	delete(providers, providerID)
	back, err := parseSettings(result)
	if err != nil {
		return snapshot{}, conflict
	}
	if len(providers) == 0 {
		if _, present := back["model_providers"]; !present {
			delete(live, "model_providers")
		}
	}
	if !reflect.DeepEqual(live, back) {
		return snapshot{}, conflict
	}
	if !s.Original.Present && len(bytes.TrimSpace(bytes.TrimPrefix(result, bom))) == 0 {
		return snapshot{}, nil
	}
	return snapshot{true, result, current.Mode}, nil
}
