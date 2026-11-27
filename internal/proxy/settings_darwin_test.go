//go:build darwin && arm64 && cgo

package proxy

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDarwinSystemProxyRestoresOnlyOwnedSettings(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "settings-fixture")
	cmd := exec.Command("xcrun", "clang", "-fobjc-arc", "-framework", "SystemConfiguration", "-framework", "Foundation",
		"testdata/system_proxy.m", "-o", executable)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile fixture: %v: %s", err, out)
	}
	if err := exec.Command(executable).Run(); err != nil {
		t.Fatal(err)
	}
}

func TestDarwinSystemProxyPreferencesLifecycle(t *testing.T) {
	if os.Getenv("KEYDOCK_MACOS_SYSTEM_PROXY_E2E") != "1" {
		t.Skip("requires a disposable macOS machine with explicit system proxy test enabled")
	}
	if os.Geteuid() != 0 {
		t.Fatal("the disposable test must run as the privileged helper user")
	}
	read := func() []byte {
		t.Helper()
		out, err := exec.Command("/usr/sbin/scutil", "--proxy").Output()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	await := func(matches func([]byte) bool) {
		t.Helper()
		for until := time.Now().Add(6 * time.Second); time.Now().Before(until); {
			if matches(read()) {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("the operating system did not publish the expected proxy configuration")
	}
	before := read()
	t.Cleanup(func() {
		if err := updateDarwinSystemProxy(false); err != nil {
			t.Error(err)
		}
		await(func(state []byte) bool { return bytes.Equal(state, before) })
	})
	for _, stop := range []string{"stop", "app-exit"} {
		t.Run(stop, func(t *testing.T) {
			fd, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
			if err != nil {
				t.Fatal(err)
			}
			client, err := tunFileConn(fd[0])
			if err != nil {
				unix.Close(fd[1])
				t.Fatal(err)
			}
			defer client.Close()
			server, err := tunFileConn(fd[1])
			if err != nil {
				t.Fatal(err)
			}
			_ = client.SetDeadline(time.Now().Add(20 * time.Second))
			done := make(chan error, 1)
			go func() {
				defer server.Close()
				err := serveDarwinTUNLease(t.Context(), server, func(ctx context.Context, body []byte, ready func() error) error {
					return runDarwinNetworkService(ctx, "", body, ready)
				})
				finishDarwinTUNLease(server, err)
				done <- err
			}()
			if err := readTUNReply(client); err != nil {
				t.Fatal(err)
			}
			if err := writeTUNFrame(client, []byte(darwinSystemProxyRequest)); err != nil {
				t.Fatal(err)
			}
			if err := readTUNReply(client); err != nil {
				t.Fatal(err)
			}
			await(func(state []byte) bool {
				for _, scheme := range []string{"HTTP", "HTTPS", "SOCKS"} {
					for _, suffix := range []string{"Enable : 1", "Port : 17890", "Proxy : 127.0.0.1"} {
						if !strings.Contains(string(state), scheme+suffix+"\n") {
							return false
						}
					}
				}
				return !strings.Contains(string(state), "ProxyAutoConfigEnable : 1") && !strings.Contains(string(state), "ProxyAutoDiscoveryEnable : 1")
			})
			if stop == "app-exit" {
				client.Close()
			} else {
				if err := client.CloseWrite(); err != nil {
					t.Fatal(err)
				}
				if err := readTUNReply(client); err != nil {
					t.Fatal(err)
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			await(func(state []byte) bool { return bytes.Equal(state, before) })
		})
	}
}
