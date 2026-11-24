package connect

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const device = "fixture-device-key"
const api = "fixture-api-key"

func registration() map[string]any {
	return map[string]any{"schema_version": 1, "device_name": "Office laptop", "api_base_url": "https://model.example.test/custom/v1", "api_key": api, "model": "test-model", "subscription_url": "https://example.test/device?format=clash"}
}
func TestFetchScopedDevice(t *testing.T) {
	var expected = registration()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.String() != "/company/api/connect/v1/device" || r.Header.Get("Authorization") != "Bearer "+device {
			t.Error("unexpected method, scope or credential")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "{}" {
			t.Error("unexpected request")
		}
		json.NewEncoder(w).Encode(expected)
	}))
	defer s.Close()
	c := Client{HTTP: s.Client()}
	fetch := func() (Profile, error) { return c.Fetch(context.Background(), s.URL+"/company/", device) }
	for _, available := range []any{registration()["subscription_url"], "", nil} {
		expected["subscription_url"] = available
		p, err := fetch()
		if err != nil {
			t.Fatal(err)
		}
		if p.Key != api || p.DeviceName != "Office laptop" || p.Model != "test-model" || p.BaseURL != "https://model.example.test/custom/v1" {
			t.Fatal("profile mismatch")
		}
		if u, _ := available.(string); p.SubscriptionURL != u {
			t.Fatal("subscription mismatch")
		}
	}
	for _, entry := range []struct {
		name  string
		value any
	}{{"schema_version", 2}, {"model", ""}, {"api_key", ""}, {"api_key", device}, {"api_base_url", "http://example.test"}, {"subscription_url", "https://secret@example.test/private"}, {"subscription_url", 42}} {
		expected = registration()
		expected[entry.name] = entry.value
		if _, err := fetch(); err == nil {
			t.Fatal(entry.name)
		}
	}
	expected = registration()
	delete(expected, "subscription_url")
	if _, err := fetch(); err == nil {
		t.Fatal("omitted subscription field accepted")
	}
}
func TestRejectsRedirectAndExcessiveResponses(t *testing.T) {
	var leaked atomic.Bool
	var mode string
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/private":
			leaked.Store(true)
			w.Write([]byte(api))
		case mode == "redirect":
			http.Redirect(w, r, "/private", http.StatusTemporaryRedirect)
		case mode == "too large":
			io.WriteString(w, strings.Repeat("x", 65<<10))
		default:
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, r.Header.Get("Authorization"))
		}
	}))
	defer s.Close()
	httpClient := s.Client()
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { leaked.Store(true); return nil }
	c := Client{HTTP: httpClient}
	for _, m := range []string{"redirect", "too large", "unauthorized"} {
		mode = m
		if _, err := c.Fetch(context.Background(), s.URL, device); err == nil || strings.Contains(err.Error(), device) || strings.Contains(err.Error(), api) {
			t.Fatal(m)
		}
	}
	mode = "redirect"
	if err := c.TestModel(context.Background(), Profile{BaseURL: s.URL, Model: "m", Key: api}); err == nil {
		t.Fatal("model redirect accepted")
	}
	if leaked.Load() {
		t.Fatal("credential-bearing redirect followed")
	}
}
func TestRejectsUnsafeEntryURLs(t *testing.T) {
	c := Client{}
	for _, u := range []string{"http://server.invalid", "https://user:pass@server.invalid", "https://server.invalid#frag", "https://server.invalid?", "https://server.invalid?key=secret", "https://server.invalid/#"} {
		if _, err := c.Fetch(context.Background(), u, device); err == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
}
func TestModelRequiresCompletedNonemptyGeneration(t *testing.T) {
	cases := []struct {
		name, typ, body string
		success         bool
	}{
		{"json", "application/json", `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}`, true},
		{"sse", "text/event-stream", "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", true},
		{"incomplete", "application/json", `{"status":"incomplete","output":[{"type":"message","content":[{"type":"output_text","text":"partial"}]}]}`, false},
		{"empty", "application/json", `{"status":"completed","output":[]}`, false},
		{"embedded error", "application/json", `{"status":"completed","error":{"message":"fixture-api-key"},"output":[{"type":"message","content":[{"type":"output_text","text":"ignored"}]}]}`, false},
		{"deltas only", "text/event-stream", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: [DONE]\n\n", false},
		{"failed stream", "text/event-stream", "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"fixture-api-key\"}}}\n\n", false},
		{"no real endpoint", "text/html", "<body>successful</body>", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/prefix/v1/responses" || r.Header.Get("Authorization") != "Bearer "+api {
					t.Error("not an authenticated Responses generation")
				}
				var v map[string]any
				if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
					t.Error(err)
				}
				if v["model"] != "assigned-model" || v["store"] != false || v["stream"] != true || v["max_output_tokens"] != float64(64) {
					t.Error("invalid test input")
				}
				w.Header().Set("Content-Type", tc.typ)
				io.WriteString(w, tc.body)
			}))
			defer s.Close()
			err := (Client{HTTP: s.Client()}).TestModel(context.Background(), Profile{Key: api, Model: "assigned-model", BaseURL: s.URL + "/prefix/v1/"})
			if (err == nil) != tc.success || err != nil && strings.Contains(err.Error(), api) {
				t.Fatalf("unexpected result %v", err)
			}
		})
	}
}
