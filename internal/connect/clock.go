package connect

import (
	"net/http"
	"time"
)

const (
	clockSkewThreshold   = 30 * time.Second
	clockMotionTolerance = 50 * time.Millisecond
)

func keyDockClockSkew(date []string, sent, received time.Time, elapsed time.Duration) time.Duration {
	if len(date) != 1 {
		return 0
	}
	server, err := http.ParseTime(date[0])
	if err != nil {
		return 0
	}
	// Compare wall time to the elapsed monotonic time before treating the two
	// samples as bounds. Tolerate small differences in clock precision and rate.
	sent = sent.Round(0)
	received = received.Round(0)
	wallElapsed := received.Sub(sent)
	if wallElapsed < 0 || elapsed < 0 {
		return 0
	}
	if motion := wallElapsed - elapsed; motion < -clockMotionTolerance || motion > clockMotionTolerance {
		return 0
	}
	// The server wrote Date at some point during the request. It reports only
	// whole seconds, so the server then read somewhere in [server, server+1s).
	// Use the offset closest to zero across both intervals.
	if fast := sent.Sub(server.Add(time.Second)); fast > clockSkewThreshold {
		return fast
	}
	if slow := server.Sub(received); slow > clockSkewThreshold {
		return -slow
	}
	return 0
}
