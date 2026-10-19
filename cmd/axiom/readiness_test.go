package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/platform/httpserver"
)

// TestReadinessFollowsTheSnapshot: a node is not ready before a snapshot can route, ready once one can, not ready while
// the snapshot is stale (when every call answers 503), and ready again once a rebuild succeeds.
func TestReadinessFollowsTheSnapshot(t *testing.T) {
	var health httpserver.Health
	ready := make(chan struct{})
	var fresh atomic.Bool
	fresh.Store(true)
	stop := announceReady(t.Context(), ready, fresh.Load, time.Millisecond, &health, slog.New(slog.DiscardHandler))
	defer stop()
	status := func() int {
		rec := httptest.NewRecorder()
		health.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code
	}
	await := func(want int, when string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for status() != want {
			if time.Now().After(deadline) {
				t.Fatalf("/readyz is %d %s, want %d", status(), when, want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	time.Sleep(20 * time.Millisecond)
	await(http.StatusServiceUnavailable, "before a snapshot can route")
	close(ready)
	await(http.StatusOK, "once a snapshot can route")
	fresh.Store(false)
	await(http.StatusServiceUnavailable, "while the snapshot is stale")
	fresh.Store(true)
	await(http.StatusOK, "after a rebuild succeeded")
}
