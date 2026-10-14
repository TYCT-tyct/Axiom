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
