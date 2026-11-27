package proxy

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestTUNCoreStartupLog(t *testing.T) {
	const timestamp = `time="2026-01-12T12:34:56.000000000Z" `
	const success = `level=info msg="[TUN] Tun adapter listening at: KeyDockTun([198.18.0.1/30],[], mtu: 1500, auto route: true"`
	for _, tc := range []struct{ name, fields, class string }{
		{"ready", success, ""},
		{"core interface lookup", tunErrorPrefix + `configure tun interface: route ip+net: no such network interface"`, "tun_core_interface_lookup"},
		{"IPv4 address", tunErrorPrefix + "configure tun interface: set ipv4 address:", "tun_core_ipv4"},
		{"DNS", tunErrorPrefix + "configure tun interface: set ipv4 dns:", "tun_core_dns"},
		{"firewall", tunErrorPrefix + "configure tun interface: FwpmFilterAdd0:", "tun_core_firewall"},
		{"other adapter failure", tunErrorPrefix + "configure tun interface: invalid argument", "tun_core_adapter"},
		{"interface monitor", tunErrorPrefix + "start DefaultInterfaceMonitor:", "tun_core_monitor"},
		{"other failure", tunErrorPrefix + "other internal error", "tun_core_initialization"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := tunCoreLog{ready: make(chan struct{}), failure: make(chan error, 1)}
			// These resemble core records but lack the required level, message prefix
			// or owned device. None may supply a startup result.
			noise := timestamp + `level=warning msg="Start TUN listening error: noise"` + "\n" +
				timestamp + `level=info msg="[TUN] Tun adapter listening at: OtherTun(...)"` + "\n" +
				timestamp + `level=error msg="proxy failed: Start TUN listening error: noise"` + "\n" +
				"garbage " + timestamp + success + "\n" +
				strings.Repeat("X", 2048) + timestamp + success + "\n"
			log.Write([]byte(noise))
			if log.reported {
				t.Fatal("accepted a non-startup core record")
			}

			// Simulate pipe reads splitting a line, a very long diagnostic suffix,
			// and multiple records in the same stream.
			secret := "synthetic-device-secret"
			data := []byte(timestamp + tc.fields + " " + strings.Repeat(secret, 50) + "\r\n" +
				timestamp + tunErrorPrefix + "ignored later error\n" + timestamp + success + "\n")
			for start := 0; start < len(data); start += 11 {
				piece := data[start:min(start+11, len(data))]
				if n, err := log.Write(piece); err != nil || n != len(piece) {
					t.Fatal("core output was not drained")
				}
			}
			if !log.reported || log.used != 0 || bytes.Contains(log.prefix[:], []byte(secret)) {
				t.Fatal("startup result missing or raw output retained")
			}
			select {
			case <-log.ready:
				if tc.class != "" {
					t.Fatal("failed startup indicated ready")
				}
			default:
				if tc.class == "" {
					t.Fatal("successful startup did not indicate ready")
				}
			}
			select {
			case err := <-log.failure:
				var classified interface{ DiagnosticClass() string }
				if tc.class == "" || !errors.As(err, &classified) || classified.DiagnosticClass() != tc.class || strings.Contains(err.Error(), secret) {
					t.Fatalf("incorrect or unsafe error class, expected %q", tc.class)
				}
			default:
				if tc.class != "" {
					t.Fatal("expected a classified startup failure")
				}
			}
		})
	}
}
