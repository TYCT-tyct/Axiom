package httpserver

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestProbeServerDropsStalledBodies sends a probe that announces a body and never sends it: the connection must close
// soon after the probe timeout instead of staying open.
func TestProbeServerDropsStalledBodies(t *testing.T) {
	t.Parallel()
	var health Health
	node := httptest.NewUnstartedServer(nil)
	node.Config = health.Server(slog.New(slog.DiscardHandler))
	node.Start()
	defer node.Close()

	conn, err := new(net.Dialer).DialContext(t.Context(), "tcp", node.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { // before node.Close, which waits for this connection
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Error(err)
		}
	}()
	if _, err := io.WriteString(conn, "GET /livez HTTP/1.1\r\nHost: probe\r\nContent-Length: 1\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := conn.SetReadDeadline(start.Add(4 * probeTimeout)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("the connection stayed open %v after the probe stalled: %v", time.Since(start), err)
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
