//go:build darwin && arm64 && cgo

package proxy

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDarwinTUNDetectsExecutableReplacedAtSamePath(t *testing.T) {
	dir := t.TempDir()
	installed, staged := filepath.Join(dir, "installed"), filepath.Join(dir, "staged")
	for _, path := range []string{installed, staged} {
		if err := os.WriteFile(path, []byte("executable fixture"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	original, err := os.Stat(installed)
	if err != nil {
		t.Fatal(err)
	}
	if darwinTUNExecutableChanged(installed, original) {
		t.Fatal("the unchanged daemon executable was considered replaced")
	}
	if err := os.Rename(staged, installed); err != nil {
		t.Fatal(err)
	}
	if !darwinTUNExecutableChanged(installed, original) {
		t.Fatal("a replacement at the same path was not detected")
	}
}

func TestDarwinTUNLeaseStopsOnDisconnect(t *testing.T) {
	for _, stop := range []string{"requested", "disconnect"} {
		t.Run(stop, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
			if err != nil {
				t.Fatal(err)
			}
			client, err := tunFileConn(fds[0])
			if err != nil {
				_ = unix.Close(fds[1])
				t.Fatal(err)
			}
			defer client.Close()
			server, err := tunFileConn(fds[1])
			if err != nil {
				t.Fatal(err)
			}
			_ = client.SetDeadline(time.Now().Add(3 * time.Second))
			done := make(chan error, 1)
			go func() {
				defer server.Close()
				err := serveDarwinTUNLease(ctx, server, func(lifetime context.Context, body []byte, ready func() error) error {
					if string(body) != "subscription fixture" {
						return errors.New("wrong request body")
					}
					if err := ready(); err != nil {
						return err
					}
					<-lifetime.Done()
					return ctx.Err()
				})
				finishDarwinTUNLease(server, err)
				done <- err
			}()
			if err := readTUNReply(client); err != nil {
				t.Fatalf("service acceptance: %v", err)
			}
			if err := writeTUNFrame(client, []byte("subscription fixture")); err != nil {
				t.Fatal(err)
			}
			if err := readTUNReply(client); err != nil {
				t.Fatalf("route readiness: %v", err)
			}
			if stop == "requested" {
				if err := client.CloseWrite(); err != nil {
					t.Fatal(err)
				}
				if err := readTUNReply(client); err != nil {
					t.Fatalf("route cleanup result: %v", err)
				}
			} else {
				_ = client.Close()
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("the owner connection did not cancel its TUN lease")
			}
		})
	}
}

func TestDarwinTUNRejectsExitBeforeReady(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	go func() {
		defer server.Close()
		err := serveDarwinTUNLease(t.Context(), server, func(context.Context, []byte, func() error) error { return nil })
		finishDarwinTUNLease(server, err)
	}()
	if err := readTUNReply(client); err != nil {
		t.Fatal(err)
	}
	if err := writeTUNFrame(client, []byte("subscription fixture")); err != nil {
		t.Fatal(err)
	}
	if err := readTUNReply(client); err == nil {
		t.Fatal("the service reported ready after the engine exited without creating the routes")
	}
}
