package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// TestIngressBoundsAStoppedCore: once a request reaches a core that then stops answering, the ingress ends it on a
// finite limit instead of holding the client until the core comes back.
func TestIngressBoundsAStoppedCore(t *testing.T) {
	t.Parallel()
	t.Run("response headers", testIngressHeaderWait)
	t.Run("response body", testIngressBodyIdle)
	t.Run("whole request", testIngressWholeRequest)
}

func testIngressHeaderWait(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer core.Close()
	defer close(release)

	transport := &http.Transport{ResponseHeaderTimeout: 50 * time.Millisecond}
	defer transport.CloseIdleConnections()
	front := httptest.NewServer(ingressHandlerWith(testCoreURL(t, core.URL), transport,
		slog.New(slog.DiscardHandler), 0, 2*time.Second))
	defer front.Close()

	start := time.Now()
	resp := getIngress(t, front.URL)
	defer closeBody(t, resp)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("the ingress waited %v for a core that sent no headers", elapsed)
	}
}

func testIngressBodyIdle(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if _, err := io.WriteString(w, "data: one\n\n"); err != nil {
			return
		}
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer core.Close()
	defer close(release)

	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	front := httptest.NewServer(ingressHandlerWith(testCoreURL(t, core.URL), transport,
		slog.New(slog.DiscardHandler), 50*time.Millisecond, 2*time.Second))
	defer front.Close()

	start := time.Now()
	resp := getIngress(t, front.URL)
	if resp.StatusCode != http.StatusOK {
		closeBody(t, resp)
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Logf("the stream ended with %v", err)
	}
	closeBody(t, resp)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("the ingress read for %v after the core went silent", elapsed)
	}
}

func testIngressWholeRequest(t *testing.T) {
	t.Parallel()
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := io.WriteString(w, "."); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		}
	}))
	defer core.Close()

	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	front := httptest.NewServer(ingressHandlerWith(testCoreURL(t, core.URL), transport,
		slog.New(slog.DiscardHandler), 500*time.Millisecond, 150*time.Millisecond))
	defer front.Close()

	start := time.Now()
	resp := getIngress(t, front.URL)
	defer closeBody(t, resp)
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Logf("the stream ended with %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("the ingress streamed for %v despite a 150ms request limit", elapsed)
	}
}

// getIngress makes a request with its own deadline, so a missing timeout fails the test instead of hanging it.
func getIngress(t *testing.T, raw string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Error(err)
	}
}

// testCoreURL parses the httptest server's URL for the ingress handler.
func testCoreURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	core, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return core
}
