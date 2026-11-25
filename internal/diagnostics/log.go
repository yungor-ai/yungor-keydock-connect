// Package diagnostics records fixed event names and typed outcomes for local support exports.
package diagnostics

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	keydockconnect "github.com/yungor-ai/yungor-keydock-connect"
)

const (
	historyName = "diagnostics-history.json"
	maxEvents   = 200
	maxHistory  = 128 << 10
)

type Event struct {
	Time       time.Time `json:"time"`
	Stage      string    `json:"stage"`
	Outcome    string    `json:"outcome"`
	DurationMS int64     `json:"duration_ms"`
	ErrorClass string    `json:"error_class,omitempty"`
	HTTPStatus int       `json:"http_status,omitempty"`
}

type State struct {
	Paired               bool   `json:"paired"`
	CodexConfigured      bool   `json:"codex_configured"`
	SystemProxyActive    bool   `json:"system_proxy_active"`
	SubscriptionAssigned bool   `json:"subscription_assigned"`
	ProxyMode            string `json:"proxy_mode,omitempty"`
}

type Log struct {
	mu     sync.Mutex
	path   string
	events []Event
}

func New(stateDir string) *Log {
	l := &Log{path: filepath.Join(stateDir, historyName), events: make([]Event, 0, maxEvents)}
	file, err := os.Open(l.path)
	if err != nil {
		return l
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxHistory+1))
	if err != nil || len(raw) > maxHistory {
		return l
	}
	var saved []Event
	if json.Unmarshal(raw, &saved) != nil {
		return l
	}
	for _, event := range saved {
		if !validStage(event.Stage) || !validOutcome(event.Outcome) || event.Time.IsZero() || event.DurationMS < 0 {
			continue
		}
		event.Time = event.Time.UTC()
		if !validErrorClass(event.ErrorClass) {
			event.ErrorClass = ""
		}
		event.HTTPStatus = validHTTPStatus(event.HTTPStatus)
		l.append(event)
	}
	return l
}

// Record retains the event in memory even if local history is temporarily unwritable.
// The error is classified by type. Its text, request URL and response contents are never read.
func (l *Log) Record(stage, outcome string, elapsed time.Duration, err error, httpStatus int) {
	if l == nil || !validStage(stage) || !validOutcome(outcome) {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var status interface{ HTTPStatus() int }
	if httpStatus == 0 && errors.As(err, &status) {
		httpStatus = status.HTTPStatus()
	}
	l.append(Event{Time: time.Now().UTC(), Stage: stage, Outcome: outcome, DurationMS: max(0, elapsed.Milliseconds()),
		ErrorClass: errorClass(err), HTTPStatus: validHTTPStatus(httpStatus)})
	if data, err := json.Marshal(l.events); err == nil {
		_ = write(l.path, data)
	}
}

func (l *Log) RecordResult(stage string, started time.Time, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	l.Record(stage, outcome, time.Since(started), err, 0)
}

func (l *Log) append(event Event) {
	if len(l.events) == maxEvents {
		copy(l.events, l.events[1:])
		l.events = l.events[:maxEvents-1]
	}
	l.events = append(l.events, event)
}

func (l *Log) Export(path string, state State) error {
	l.mu.Lock()
	events := append([]Event{}, l.events...)
	l.mu.Unlock()
	data, err := json.MarshalIndent(struct {
		SchemaVersion int       `json:"schema_version"`
		AppVersion    string    `json:"app_version"`
		OS            string    `json:"os"`
		Arch          string    `json:"arch"`
		OSVersion     string    `json:"os_version,omitempty"`
		CollectedAt   time.Time `json:"collected_at"`
		State         State     `json:"state"`
		Events        []Event   `json:"events"`
	}{
		SchemaVersion: 1,
		AppVersion:    keydockconnect.Version(),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		OSVersion:     osVersion(),
		CollectedAt:   time.Now().UTC(),
		State:         state,
		Events:        events,
	}, "", "  ")
	if err != nil {
		return err
	}
	return write(path, append(data, '\n'))
}

func write(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".keydock-diagnostics-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func validStage(value string) bool {
	switch value {
	case "app.start", "app.enrollment_load", "app.window", "device", "codex", "model", "proxy", "proxy.enable", "proxy.disable", "proxy.recover",
		"codex.restore", "codex.launch", "app.shutdown", "proxy.subscription", "proxy.local_port", "proxy.profile", "proxy.core_integrity",
		"proxy.core_data", "proxy.core_process", "proxy.core_job", "proxy.core_ready", "proxy.settings_apply", "proxy.settings_restore",
		"proxy.worker_exit", "proxy.test_local_dial", "proxy.test_connect", "proxy.test_tls", "proxy.test_https", "proxy.subscription_refresh",
		"proxy.tun_ready", "proxy.tun_cleanup", "proxy.refresh", "proxy.subscription_status":
		return true
	}
	return false
}

func validOutcome(value string) bool {
	switch value {
	case "start", "ok", "failed", "skipped":
		return true
	}
	return false
}

func validHTTPStatus(value int) int {
	if value >= 100 && value <= 599 {
		return value
	}
	return 0
}

func validErrorClass(value string) bool {
	switch value {
	case "", "canceled", "timeout", "dns", "tls_certificate", "tls_protocol", "connection_refused", "connection_reset", "network_unreachable", "permission", "missing_file", "eof", "other":
		return true
	}
	return false
}

func errorClass(err error) string {
	var dns *net.DNSError
	var verification *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var header tls.RecordHeaderError
	var alert tls.AlertError
	var network net.Error
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &dns):
		return "dns"
	case errors.As(err, &verification), errors.As(err, &authority), errors.As(err, &hostname), errors.As(err, &invalid):
		return "tls_certificate"
	case errors.As(err, &header), errors.As(err, &alert):
		return "tls_protocol"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNABORTED):
		return "connection_reset"
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		return "network_unreachable"
	case errors.Is(err, os.ErrPermission):
		return "permission"
	case errors.Is(err, os.ErrNotExist):
		return "missing_file"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "eof"
	case errors.As(err, &network) && network.Timeout():
		return "timeout"
	}
	if kind := platformNetworkError(err); kind != "" {
		return kind
	}
	return "other"
}
