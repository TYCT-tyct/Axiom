package httpserver

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestServerLimits: every server drops connections that never finish their headers and keep-alive connections a
// client abandoned. Only the probes bound a whole request and response: a public response streams as long as the
// model does.
func TestServerLimits(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	var health Health
	servers := []struct {
		name    string
		server  *http.Server
		streams bool
	}{
		{"New", New(http.NotFoundHandler(), logger), true},
		{"the probes", health.Server(logger), false},
	}
	for _, s := range servers {
		unbounded := s.server.ReadTimeout == 0 && s.server.WriteTimeout == 0
		bounded := s.server.ReadTimeout > 0 && s.server.WriteTimeout > 0
		if s.server.ReadHeaderTimeout <= 0 || s.server.IdleTimeout <= 0 || s.streams && !unbounded || !s.streams && !bounded {
			t.Errorf("%s: header %v, idle %v, read %v, write %v", s.name, s.server.ReadHeaderTimeout,
				s.server.IdleTimeout, s.server.ReadTimeout, s.server.WriteTimeout)
		}
	}
}

func TestHealth(t *testing.T) {
	var health Health
	handler := health.Handler()
	status := func(method, path string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
		return rec.Code
	}
	checks := []struct {
		name         string
		ready        bool
		method, path string
		want         int
	}{
		{"alive while starting", false, http.MethodGet, "/livez", http.StatusOK},
		{"not ready while starting", false, http.MethodGet, "/readyz", http.StatusServiceUnavailable},
		{"ready", true, http.MethodGet, "/readyz", http.StatusOK},
		{"HEAD works for probes", true, http.MethodHead, "/readyz", http.StatusOK},
		{"not ready once shutdown begins", false, http.MethodGet, "/readyz", http.StatusServiceUnavailable},
		{"alive while shutting down", false, http.MethodGet, "/livez", http.StatusOK},
		{"nothing else", true, http.MethodGet, "/", http.StatusNotFound},
		{"read only", true, http.MethodPost, "/readyz", http.StatusMethodNotAllowed},
	}
	for _, c := range checks {
		health.SetReady(c.ready)
		if got := status(c.method, c.path); got != c.want {
			t.Errorf("%s: %s %s = %d, want %d", c.name, c.method, c.path, got, c.want)
		}
	}
}

// TestGate: a request that would start new work is refused while the node drains, with an answer a load balancer that
// has not removed the node yet turns into a retry; before the drain and once it ends it passes through.
func TestGate(t *testing.T) {
	var health Health
	gate := health.Gate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	serve := func(method string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, "/", nil))
		return rec
	}
	if rec := serve(http.MethodGet); rec.Code != http.StatusNoContent {
		t.Errorf("before the drain: %d", rec.Code)
	}
	health.SetReady(false) // not ready, still not draining: startup is not a reason to refuse a page
	if rec := serve(http.MethodGet); rec.Code != http.StatusNoContent {
		t.Errorf("not ready but not draining: %d", rec.Code)
	}
	health.BeginDrain()
	if health.Ready() {
		t.Error("draining while ready")
	}
	rec := serve(http.MethodGet)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" ||
		rec.Header().Get("Cache-Control") != "no-store" || rec.Body.String() != "the node is draining\n" {
		t.Errorf("during the drain: %d %q (%q, %q)", rec.Code, rec.Body.String(), rec.Header().Get("Retry-After"),
			rec.Header().Get("Cache-Control"))
	}
	if head := serve(http.MethodHead); head.Body.Len() != 0 {
		t.Errorf("HEAD carried a body: %q", head.Body.String())
	}
}
