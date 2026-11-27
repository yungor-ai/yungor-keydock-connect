//go:build darwin && arm64

package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	poisonedDNSAnswer = "198.18.250.230"
	splitDNSAnswer    = "100.96.0.46"
)

// The default resolver simulates a router serving foreign fake IPs. The more
// specific resolver simulates a VPN split DNS policy. Only run on disposable hosts.
func TestDarwinTUNSystemDNSLifecycle(t *testing.T) {
	if os.Getenv("KEYDOCK_DARWIN_DNS_E2E") != "1" {
		t.Skip("requires a disposable macOS runner")
	}
	core := os.Getenv("KEYDOCK_TEST_CORE")
	if os.Geteuid() != 0 || core == "" {
		t.Fatal("run as root with the packaged core")
	}
	domain := fmt.Sprintf("kd-%d.example.com", time.Now().UnixNano())
	split := "office." + domain
	for _, stop := range []string{"normal", "crash"} {
		t.Run(stop, func(t *testing.T) {
			useFixtureSystemDNS(t, core, domain, split)
			// Query fresh names at every stage so a cached answer cannot pass.
			stage := stop + "." + domain
			splitStage := stop + "." + split
			if _, err := systemDNSAnswer(t.Context(), "before."+stage, poisonedDNSAnswer); err != nil {
				t.Fatal(err)
			}
			if _, err := systemDNSAnswer(t.Context(), "before."+splitStage, splitDNSAnswer); err != nil {
				t.Fatal(err)
			}
			host := "during." + stage
			peer := observingDNSPeer(t, core, host)
			subscription := strings.Replace(peerSubscription(peer), "MATCH,test-node", "DOMAIN,"+host+",test-node\n  - MATCH,DIRECT", 1)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var probeErr error
			err := runDarwinTUN(ctx, core, []byte(subscription), func() error {
				destination, err := systemDNSAnswer(ctx, host, "")
				if err == nil {
					err = probeDNSPeer(destination)
				}
				if err == nil {
					_, err = systemDNSAnswer(ctx, "during."+splitStage, splitDNSAnswer)
				}
				if err == nil && stop == "crash" {
					err = killFixtureTUNCore(ctx)
					if err == nil {
						return nil
					}
				}
				probeErr = err
				cancel()
				return nil
			})
			if probeErr != nil {
				t.Fatal(probeErr)
			}
			if stop == "normal" && err != nil || stop == "crash" && (err == nil || !strings.Contains(err.Error(), "代理核心意外退出")) {
				t.Fatalf("%s stop returned %v", stop, err)
			}
			if _, err := systemDNSAnswer(t.Context(), "after."+stage, poisonedDNSAnswer); err != nil {
				t.Error(err)
			}
			if _, err := systemDNSAnswer(t.Context(), "after."+splitStage, splitDNSAnswer); err != nil {
				t.Error(err)
			}
		})
	}
}

func useFixtureSystemDNS(t *testing.T, core, domain, split string) {
	t.Helper()
	// Read the upstream before replacing the default DNS, so the resolver can
	// forward unrelated traffic for the runner through its original nameserver.
	resolv, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		t.Fatal(err)
	}
	var upstream netip.Addr
	for line := range strings.SplitSeq(string(resolv), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "nameserver" {
			upstream, err = netip.ParseAddr(f[1])
			if err == nil && !upstream.IsLoopback() {
				break
			}
		}
	}
	if !upstream.IsValid() || upstream.IsLoopback() {
		t.Fatal("disposable runner has no upstream nameserver")
	}
	startDNS := func(zone, answer string, port int) {
		t.Helper()
		config := fmt.Sprintf(`hosts:
  '+.%s': %s
dns:
  enable: true
  enhanced-mode: redir-host
  listen: 127.0.0.1:%d
  nameserver: ["%s"]
`, zone, answer, port, upstream)
		runFixtureCore(t, core, []byte(config), port)
	}
	startDNS(domain, poisonedDNSAnswer, 53)
	splitPort := reservePort(t)
	startDNS(split, splitDNSAnswer, splitPort)

	// Select the primary network service by ID, independent of its UI label.
	field := func(key, property string) string {
		t.Helper()
		cmd := exec.Command("/usr/sbin/scutil")
		cmd.Stdin = strings.NewReader("show " + key + "\n")
		data, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			if val, ok := strings.CutPrefix(strings.TrimSpace(line), property+" : "); ok {
				return val
			}
		}
		t.Fatalf("missing %s on %s", property, key)
		return ""
	}
	service := field("Setup:/Network/Service/"+field("State:/Network/Global/IPv4", "PrimaryService"), "UserDefinedName")
	settings := func(args ...string) (string, error) {
		output, err := exec.Command("/usr/sbin/networksetup", args...).CombinedOutput()
		if err == nil && bytes.Contains(output, []byte("** Error:")) {
			err = errors.New(strings.TrimSpace(string(output)))
		}
		return strings.TrimSpace(string(output)), err
	}
	prior, err := settings("-getdnsservers", service)
	if err != nil {
		t.Fatal(err)
	}
	restore := []string{"-setdnsservers", service}
	if strings.HasPrefix(prior, "There aren't any DNS Servers set on ") {
		restore = append(restore, "Empty") // DHCP-provided DNS
	} else {
		for _, addr := range strings.Fields(prior) {
			if _, err := netip.ParseAddr(addr); err != nil {
				t.Fatalf("unexpected networksetup DNS output %q", prior)
			}
			restore = append(restore, addr)
		}
		if len(restore) == 2 {
			t.Fatal("empty networksetup DNS output")
		}
	}
	t.Cleanup(func() {
		if _, err := settings(restore...); err != nil {
			t.Errorf("restoring primary service DNS: %v", err)
		}
	})
	if _, err := settings("-setdnsservers", service, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	const dir = "/etc/resolver"
	if err := os.Mkdir(dir, 0755); err == nil {
		t.Cleanup(func() { _ = os.Remove(dir) })
	} else if !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	path := filepath.Join(dir, split)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	_, err = fmt.Fprintf(f, "nameserver 127.0.0.1\nport %d\n", splitPort)
	if err = errors.Join(err, f.Close()); err != nil {
		t.Fatal(err)
	}
}

var fixtureFakeRange = netip.MustParsePrefix("198.18.0.0/16")

// dscacheutil uses the macOS system resolver, including its split DNS policy.
func systemDNSAnswer(ctx context.Context, host, want string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	var last string
	for ctx.Err() == nil {
		out, _ := exec.CommandContext(ctx, "/usr/bin/dscacheutil", "-q", "host", "-a", "name", host).Output()
		last = strings.TrimSpace(string(out))
		for line := range strings.SplitSeq(last, "\n") {
			if s, ok := strings.CutPrefix(line, "ip_address: "); ok {
				addr, err := netip.ParseAddr(s)
				if s == want || want == "" && err == nil && s != poisonedDNSAnswer && fixtureFakeRange.Contains(addr) {
					return s, nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("system DNS for %s did not return %q (empty means KeyDock fake IP), last response %q", host, want, last)
}

// The synthetic SS2022 exit accepts only the expected domain as its CONNECT
// destination. A numeric IP, a DNS query, or an inferred application hostname
// cannot satisfy the echo check below.
func observingDNSPeer(t *testing.T, core, host string) int {
	t.Helper()
	exit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != net.JoinHostPort(host, "32481") {
			http.Error(w, "unexpected destination", http.StatusBadGateway)
			return
		}
		client, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer client.Close()
		_ = client.SetDeadline(time.Now().Add(4 * time.Second))
		fmt.Fprint(buf, "HTTP/1.1 200 Connection Established\r\n\r\n")
		if err := buf.Flush(); err == nil {
			io.CopyN(client, buf, 1)
		}
	}))
	t.Cleanup(exit.Close)
	port := reservePort(t)
	cfg := fmt.Sprintf(`listeners:
  - name: fixture-ss
    type: shadowsocks
    listen: 127.0.0.1
    port: %d
    cipher: 2022-blake3-aes-128-gcm
    password: "AAAAAAAAAAAAAAAAAAAAAA=="
    users: {test-device: "AQEBAQEBAQEBAQEBAQEBAQ=="}
proxies:
  - {name: observer, type: http, server: 127.0.0.1, port: %d}
rules: ['MATCH,observer']
`, port, exit.Listener.Addr().(*net.TCPAddr).Port)
	runFixtureCore(t, core, []byte(cfg), port)
	return port
}

func probeDNSPeer(address string) error {
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort(address, "32481"), 4*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := conn.Write([]byte{'K'}); err != nil {
		return err
	}
	var echo [1]byte
	if _, err := io.ReadFull(conn, echo[:]); err != nil || echo[0] != 'K' {
		return fmt.Errorf("SS exit did not echo traffic addressed by the original domain: byte %x, error %v", echo, err)
	}
	return nil
}

func killFixtureTUNCore(ctx context.Context) error {
	// Match only the root-owned runtime copy spawned directly by this test.
	out, err := exec.CommandContext(ctx, "/usr/bin/pgrep", "-P", strconv.Itoa(os.Getpid()), "-f", "^/private/var/tmp/keydock-tun-[^/]*/mihomo ").Output()
	if err != nil {
		return fmt.Errorf("find TUN core child: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 1 {
		return fmt.Errorf("expected one TUN core child, got %q", out)
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}
