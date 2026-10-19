package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"axiom/internal/platform/httpserver"
)

// TestReadinessFollowsTheSnapshot: a node is not ready before a snapshot can route, ready once one can, not ready while
// the snapshot is stale (every call answers 503) or when every endpoint is excluded (every call answers 404), and
// ready again once a rebuild fixes it.
func TestReadinessFollowsTheSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var health httpserver.Health
		var routable, fresh atomic.Bool
		ready := func() bool { return routable.Load() && fresh.Load() }
		stop := announceReady(t.Context(), ready, time.Second, &health, slog.New(slog.DiscardHandler))
		defer stop()
		check := func(want int, when string) {
			t.Helper()
			time.Sleep(time.Second) // one tick; the announcer reads the state, then blocks again
			synctest.Wait()
			rec := httptest.NewRecorder()
			health.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))
			if rec.Code != want {
				t.Errorf("/readyz is %d %s, want %d", rec.Code, when, want)
			}
		}
		synctest.Wait()
		check(http.StatusServiceUnavailable, "before a snapshot can route")
		routable.Store(true)
		fresh.Store(true)
		check(http.StatusOK, "once a snapshot can route")
		fresh.Store(false)
		check(http.StatusServiceUnavailable, "while the snapshot is stale")
		fresh.Store(true)
		check(http.StatusOK, "after a rebuild succeeded")
		routable.Store(false)
		check(http.StatusServiceUnavailable, "when every endpoint is excluded")
		routable.Store(true)
		check(http.StatusOK, "after the endpoints are routable again")
	})
}
