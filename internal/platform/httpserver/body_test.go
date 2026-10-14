package httpserver

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStalledBodiesLoseTheirConnection: a request that announces a body and never sends it loses its connection
// soon after its limit, even when the handler never reads the body: behind Public and on the probe server.
func TestStalledBodiesLoseTheirConnection(t *testing.T) {
	t.Parallel()
	node, _ := public(t, map[string]http.HandlerFunc{"GET /": func(http.ResponseWriter, *http.Request) {}})
	var health Health
	probes := httptest.NewUnstartedServer(nil)
	probes.Config = health.Server(slog.New(slog.DiscardHandler))
	probes.Start()
	t.Cleanup(probes.Close)
	cases := []struct {
		name, addr, path string
		limit            time.Duration
	}{
		{"public", node.Listener.Addr().String(), "/", bodyTimeout},
		{"probes", probes.Listener.Addr().String(), "/livez", probeTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { stallBody(t, c.addr, c.path, c.limit) })
	}
}

// stallBody announces a body to path and never sends it, and fails unless the server closes the connection soon
// after limit.
func stallBody(t *testing.T, addr, path string, limit time.Duration) {
	t.Helper()
	conn, err := new(net.Dialer).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { // before the server's Close, which waits for this connection
		if err := conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Error(err)
		}
	}()
	if _, err := io.WriteString(conn, "GET "+path+" HTTP/1.1\r\nHost: node\r\nContent-Length: 1\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := conn.SetReadDeadline(start.Add(limit + 3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("the connection stayed open %v after the body stalled: %v", time.Since(start), err)
	}
}

// TestPublicLongRequestsOutliveTheBodyDeadline: a handler that runs well past the body deadline keeps its context
// and finishes its response, whether the request had a body (read in full) or none.
func TestPublicLongRequestsOutliveTheBodyDeadline(t *testing.T) {
	slow := func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		select {
		case <-time.After(3 * bodyTimeout):
		case <-r.Context().Done():
			t.Errorf("%s %s: the context ended after %d body bytes: %v", r.Method, r.URL.Path, len(body), r.Context().Err())
		}
		write(t, w, "done")
	}
	node, _ := public(t, map[string]http.HandlerFunc{"/": slow})
	requests := map[string]func() (*http.Request, error){
		"with a body": func() (*http.Request, error) {
			return http.NewRequestWithContext(t.Context(), http.MethodPost, node.URL, strings.NewReader("payload"))
		},
		"without a body": func() (*http.Request, error) {
			return http.NewRequestWithContext(t.Context(), http.MethodGet, node.URL, nil)
		},
	}
	for name, request := range requests {
		req, err := request()
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := io.ReadAll(resp.Body)
		if closeErr := resp.Body.Close(); err == nil {
			err = closeErr
		}
		if err != nil || string(got) != "done" {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
}
