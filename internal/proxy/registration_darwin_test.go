//go:build darwin && arm64 && cgo

package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	connect "github.com/yungor-ai/yungor-keydock-connect"
)

func TestDarwinRegistrationMigration(t *testing.T) {
	busy := errors.New("another session owns the helper")
	for _, tt := range []struct {
		name       string
		replies    []int
		reserveErr error
		reserves   int
		replace    bool
		failure    bool
	}{
		{"current while another session runs", []int{1}, nil, 0, false, false},
		{"v046 retires before new registration is reserved", []int{3, 0, 2}, nil, 1, true, false},
		{"never unregister an occupied helper", []int{2}, busy, 1, false, true},
		{"different app copy", []int{4}, nil, 0, false, true},
		{"unreachable", []int{0}, nil, 0, false, true},
		{"active v046 stays registered", []int{3}, nil, 0, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
			defer cancel()
			reserves, i := 0, 0
			release, err := prepareDarwinRegistration(ctx, func() int {
				i++
				return tt.replies[min(i, len(tt.replies))-1]
			}, func() (func(), error) {
				reserves++
				if tt.reserveErr != nil {
					return nil, tt.reserveErr
				}
				return func() {}, nil
			}, time.Millisecond)
			if (release != nil) != tt.replace || (err != nil) != tt.failure || reserves != tt.reserves {
				t.Fatalf("replacement=%v error=%v holds=%d", release != nil, err, reserves)
			}
			if tt.reserveErr != nil && !errors.Is(err, tt.reserveErr) {
				t.Fatal("busy session error was lost")
			}
		})
	}
}

func TestDarwinStaleRegistrationOnlyReservesIdleLease(t *testing.T) {
	legacy := []string{"--tun-daemon"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := serveRegisteredDarwinService(ctx, legacy, "", []byte(darwinSystemProxyRequest), func() error {
		t.Fatal("stale registration was allowed to set network preferences")
		return nil
	}); err == nil {
		t.Fatal("old registration started an operation")
	}
	if err := serveRegisteredDarwinService(ctx, []string{"--tun-daemon", connect.Version()}, "", []byte(darwinRegisterRequest), func() error {
		t.Fatal("current registration was eligible for removal")
		return nil
	}); err == nil {
		t.Fatal("current registration granted a migration lease")
	}
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serveRegisteredDarwinService(ctx, legacy, "", []byte(darwinRegisterRequest), func() error {
			close(ready)
			return nil
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("reservation ended before idle confirmation: %v", err)
	case <-time.After(time.Second):
		t.Fatal("no reservation was established")
	}
	select {
	case <-done:
		t.Fatal("reservation released before the owner finished")
	default:
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
