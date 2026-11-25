package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExportRetainsBoundedSafeHistoryAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	log := New(dir)
	for i := 0; i < 205; i++ {
		log.Record("device", "ok", time.Duration(i)*time.Millisecond, nil, 0)
	}
	const private = "secret-profile-token"
	testErr := &url.Error{Op: "POST", URL: "https://private.example/" + private,
		Err: &net.DNSError{Err: private, Name: "private.example"}}
	log.Record("proxy.subscription", "failed", 42*time.Millisecond, testErr, 403)
	log.Record("proxy.test_https", "failed", 50*time.Millisecond, fmt.Errorf("%s: %w", private, context.DeadlineExceeded), 0)
	log.Record(private, "failed", 0, testErr, 0)
	log.Record("device", private, 0, testErr, 0)

	path := filepath.Join(t.TempDir(), "report.json")
	if err := New(dir).Export(path, State{Paired: true}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), private) || strings.Contains(string(raw), "private.example") {
		t.Fatal("export includes private error text or URL")
	}
	var report struct {
		SchemaVersion int     `json:"schema_version"`
		AppVersion    string  `json:"app_version"`
		State         State   `json:"state"`
		Events        []Event `json:"events"`
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || report.AppVersion == "" || !report.State.Paired || len(report.Events) != 200 {
		t.Fatalf("invalid report metadata or retention: schema=%d version=%s events=%d", report.SchemaVersion, report.AppVersion, len(report.Events))
	}
	if first := report.Events[0]; first.Stage != "device" || first.DurationMS != 7 {
		t.Fatalf("did not retain newest events: %+v", first)
	}
	if event := report.Events[198]; event.ErrorClass != "dns" || event.HTTPStatus != 403 || event.DurationMS != 42 {
		t.Fatalf("unexpected subscription diagnostics: %+v", event)
	}
	if event := report.Events[199]; event.ErrorClass != "timeout" || event.Stage != "proxy.test_https" {
		t.Fatalf("unexpected probe diagnostics: %+v", event)
	}
}

func TestExportFiltersUnrecognizedStoredFields(t *testing.T) {
	dir := t.TempDir()
	raw := `[{"time":"2026-11-25T03:00:00Z","stage":"device","outcome":"ok","duration_ms":1,"detail":"private-field"},
{"time":"2026-11-25T03:00:00Z","stage":"model","outcome":"failed","duration_ms":11,"error_class":"private-class"},
{"time":"2026-11-25T03:00:00Z","stage":"private-stage","outcome":"ok","duration_ms":1}]`
	if err := os.WriteFile(filepath.Join(dir, historyName), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := New(dir).Export(path, State{}); err != nil {
		t.Fatal(err)
	}
	exported, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(exported), "private") || !strings.Contains(string(exported), `"stage": "device"`) {
		t.Fatalf("expected only known diagnostic fields: %s", exported)
	}
}
