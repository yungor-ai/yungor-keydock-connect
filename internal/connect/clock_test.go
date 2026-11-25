package connect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestKeyDockClockSkewBounds(t *testing.T) {
	server := time.Date(2026, time.November, 25, 7, 35, 44, 0, time.UTC)
	date := []string{server.Format(http.TimeFormat)}
	for _, tc := range []struct {
		name            string
		header          []string
		sentMS, recvMS  int
		elapsedMS, want int
	}{
		{"confirmed slow Windows clock", date, -31400, -31324, 76, -31324},
		{"confirmed fast clock", date, 32400, 32500, 100, 31400},
		{"zero difference", date, 0, 100, 100, 0},
		{"slow ambiguous network delay", date, -31500, -29900, 1600, 0},
		{"fast ambiguous Date precision", date, 30900, 31000, 100, 0},
		{"exactly threshold", date, -30100, -30000, 100, 0},
		{"missing Date", nil, -31400, -31324, 76, 0},
		{"invalid Date", []string{"not an HTTP date"}, -31400, -31324, 76, 0},
		{"multiple Date values", []string{date[0], date[0]}, -31400, -31324, 76, 0},
		{"wall clock moved back", date, -31324, -31400, 76, 0},
		{"wall clock jumped ahead", date, 32400, 33500, 100, 0},
		{"wall clock fell behind", date, -31400, -31324, 1076, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sent := server.Add(time.Duration(tc.sentMS) * time.Millisecond)
			received := server.Add(time.Duration(tc.recvMS) * time.Millisecond)
			want := time.Duration(tc.want) * time.Millisecond
			if got := keyDockClockSkew(tc.header, sent, received, time.Duration(tc.elapsedMS)*time.Millisecond); got != want {
				t.Fatalf("clock skew = %s, want %s", got, want)
			}
		})
	}
}

func TestFetchReportsClockSkewOnlyFromSuccessfulDeviceResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", time.Now().UTC().Add(40*time.Second).Format(http.TimeFormat))
		if r.Header.Get("Authorization") != "Bearer "+device {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(registration())
	}))
	defer server.Close()
	client := Client{HTTP: server.Client()}
	got, err := client.Fetch(context.Background(), server.URL, device)
	if err != nil || got.Profile.Key != api || got.ClockSkew >= -30*time.Second {
		t.Fatalf("expected an authenticated profile and a slow-clock result, got %s, error %v", got.ClockSkew, err)
	}
	denied, err := client.Fetch(context.Background(), server.URL, "wrong-fixture-key")
	if err == nil || denied.ClockSkew != 0 {
		t.Fatalf("unauthorized response reported clock skew %s, error %v", denied.ClockSkew, err)
	}
}
