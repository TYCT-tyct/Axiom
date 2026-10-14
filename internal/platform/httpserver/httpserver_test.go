package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
