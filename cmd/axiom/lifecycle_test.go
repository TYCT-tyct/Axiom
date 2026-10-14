package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"axiom/internal/platform/httpserver"
)

// TestShutdownDrainsPublicFirst: with a request in flight on the public listener, shutdown reports not ready while
// the probes still answer, lets the request finish, and only then stops the probes.
func TestShutdownDrainsPublicFirst(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	started, release := make(chan struct{}), make(chan struct{})
	slow := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		if _, err := io.WriteString(w, "finished"); err != nil {
			t.Error(err)
		}
	})
	var health httpserver.Health
	health.SetReady(true)
	servers := []*listening{
		{"AXIOM_PUBLIC_ADDR", "127.0.0.1:0", httpserver.New(slow, logger), nil},
		{"AXIOM_OPS_ADDR", "127.0.0.1:0", health.Server(logger), nil},
	}
	if err := listen(t.Context(), servers); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, len(servers))
	for _, s := range servers {
		go func() { served <- s.server.Serve(s.listener) }()
	}
	public, ops := "http://"+servers[0].listener.Addr().String(), "http://"+servers[1].listener.Addr().String()

	inFlight := make(chan response, 1)
	go func() {
		resp, err := get(t, public+"/")
		if err != nil {
			t.Error(err)
		}
		inFlight <- resp
	}()
	<-started
	stopped := make(chan error, 1)
	go func() { stopped <- shutdown(t.Context(), 10*time.Second, &health, servers) }()

	waitFor(t, func() bool {
		resp, err := get(t, ops+"/readyz")
		return err == nil && resp.StatusCode == http.StatusServiceUnavailable
	})
	close(release)
	if resp := <-inFlight; string(resp.body) != "finished" {
		t.Errorf("the request in flight got %q", resp.body)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, ops+"/livez"); err == nil {
		t.Error("the probes still answer after shutdown")
	}
	for range servers {
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	}
}

// TestListenReleasesWhatItBound: when a later address is taken, the listeners already bound are closed again.
func TestListenReleasesWhatItBound(t *testing.T) {
	taken, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taken.Close(); err != nil {
			t.Error(err)
		}
	})
	free, logger := freePort(t), slog.New(slog.DiscardHandler)
	servers := []*listening{
		{"AXIOM_PUBLIC_ADDR", free, httpserver.New(http.NotFoundHandler(), logger), nil},
		{"AXIOM_OPS_ADDR", taken.Addr().String(), httpserver.New(http.NotFoundHandler(), logger), nil},
	}
	if err := listen(t.Context(), servers); err == nil {
		t.Fatal("listen succeeded on a taken address")
	}
	again, err := new(net.ListenConfig).Listen(t.Context(), "tcp", free)
	if err != nil {
		t.Fatalf("the first address stayed bound: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Error(err)
	}
}

// freePort returns an address nothing listens on right now.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// waitFor polls until done holds, for at most five seconds.
func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for !done() {
		select {
		case <-ctx.Done():
			t.Fatal("the condition never held")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
