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
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
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
	servers := node("127.0.0.1:0", slow, "127.0.0.1:0", slow, "127.0.0.1:0", &health, logger, httpserver.Telemetry{})
	if err := listen(t.Context(), servers); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, len(servers))
	for _, s := range servers {
		go func() { served <- s.server.Serve(s.listener) }()
	}
	urls := map[string]string{}
	for _, s := range servers {
		urls[s.variable] = "http://" + s.listener.Addr().String()
	}
	public, ops := urls["AXIOM_PUBLIC_ADDR"], urls["AXIOM_OPS_ADDR"]

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

// TestShutdownEndsRequestsPastTheLimit: a request still holding a database connection when the limit runs out is
// ended, so the pool closes and the node stops within about the limit instead of waiting for the request.
func TestShutdownEndsRequestsPastTheLimit(t *testing.T) {
	db, err := pg.Open(t.Context(), pgtest.New(t, modules()...).App)
	if err != nil {
		t.Fatal(err)
	}
	started, ended := make(chan struct{}), make(chan error, 1)
	stuck := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		ctx := r.Context()
		ended <- db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
			return tx.QueryRow(ctx, "SELECT 1 FROM pg_sleep(60)").Scan(new(int))
		})
	})
	var health httpserver.Health
	servers := []*listening{{"AXIOM_PUBLIC_ADDR", "127.0.0.1:0", httpserver.New(stuck, slog.New(slog.DiscardHandler)), nil}}
	if err := listen(t.Context(), servers); err != nil {
		t.Fatal(err)
	}
	served, answered := make(chan error, 1), make(chan error, 1)
	go func() { served <- servers[0].server.Serve(servers[0].listener) }()
	// Unlike get, the client waits as long as the test does: only the server may end the request.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+servers[0].listener.Addr().String()+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			err = resp.Body.Close()
		}
		answered <- err
	}()
	<-started

	start := time.Now()
	err = shutdown(t.Context(), time.Second, &health, servers)
	db.Close()
	if elapsed := time.Since(start); elapsed > 5*time.Second || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("shutdown and closing the pool took %v: %v", elapsed, err)
	}
	if err := <-ended; err == nil {
		t.Error("the query outlived shutdown")
	}
	if err := <-answered; err == nil {
		t.Error("the request past the limit was answered")
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		t.Error(err)
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
