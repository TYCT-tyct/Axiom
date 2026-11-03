package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// testNode is a core node's servers listening on loopback, with the health they gate on.
type testNode struct {
	health      *httpserver.Health
	servers     []*listening
	public, ops string
	served      chan error
}

// serveNode starts a core node's servers around handler. The public and admin listeners both use it.
func serveNode(t *testing.T, handler http.Handler) *testNode {
	t.Helper()
	health := new(httpserver.Health)
	health.SetReady(true)
	cfg := nodeSettings{roles: nodeRoles{roleCore}, publicAddr: "127.0.0.1:0", adminAddr: "127.0.0.1:0", probesAddr: "127.0.0.1:0"}
	servers := listeners(cfg)
	node(servers, cfg, handler, handler, health, slog.New(slog.DiscardHandler), httpserver.Telemetry{})
	if err := listen(t.Context(), servers); err != nil {
		t.Fatal(err)
	}
	n := &testNode{health: health, servers: servers, served: make(chan error, len(servers))}
	for _, s := range servers {
		go func() { n.served <- s.server.Serve(s.listener) }()
	}
	urls := map[string]string{}
	for _, s := range servers {
		urls[s.variable] = "http://" + s.listener.Addr().String()
	}
	n.public, n.ops = urls["AXIOM_PUBLIC_ADDR"], urls["AXIOM_PROBES_ADDR"]
	return n
}

// awaitClosed reads the servers' Serve results, which must all be the close shutdown causes.
func (n *testNode) awaitClosed(t *testing.T) {
	t.Helper()
	for range n.servers {
		if err := <-n.served; !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	}
}

// TestShutdownDrainsPublicFirst: with a request in flight on the public listener, the drain reports not ready while
// the probes still answer, lets the request finish, and only then stops the probes.
func TestShutdownDrainsPublicFirst(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}), make(chan struct{})
	slow := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		if _, err := io.WriteString(w, "finished"); err != nil {
			t.Error(err)
		}
	})
	n := serveNode(t, slow)
	inFlight := make(chan response, 1)
	go func() {
		resp, err := get(t, n.public+"/")
		if err != nil {
			t.Error(err)
		}
		inFlight <- resp
	}()
	<-started
	beginDrain(n.health, 0)
	waitFor(t, func() bool {
		resp, err := get(t, n.ops+"/readyz")
		return err == nil && resp.StatusCode == http.StatusServiceUnavailable
	})
	drainCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- shutdown(drainCtx, n.servers) }()
	close(release)
	if resp := <-inFlight; string(resp.body) != "finished" {
		t.Errorf("the request in flight got %q", resp.body)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if _, err := get(t, n.ops+"/livez"); err == nil {
		t.Error("the probes still answer after shutdown")
	}
	n.awaitClosed(t)
}

// TestDrainRefusesNewRequests: while the node drains, a request that would start new work gets a retryable 503 from
// the still-open listener, so a load balancer that has not removed the node yet sends it somewhere else.
func TestDrainRefusesNewRequests(t *testing.T) {
	t.Parallel()
	n := serveNode(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("a new request was served") }))
	beginDrain(n.health, 0)
	for _, target := range []string{n.public + "/", n.public + "/assets/index-AAAAAAAA.js"} {
		resp, err := get(t, target)
		if err != nil || resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "1" ||
			resp.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("%s during the drain: %v %v", target, resp.Response, err)
		}
	}
	drainCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := shutdown(drainCtx, n.servers); err != nil {
		t.Fatal(err)
	}
	n.awaitClosed(t)
}

// TestDrainDelayWaitsOnlyForAReadyNode: a ready node reports not ready at once and keeps its listeners open for the
// edge to stop routing here; one that was never ready has no traffic to wait for.
func TestDrainDelayWaitsOnlyForAReadyNode(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		var health httpserver.Health
		health.SetReady(true)
		drained := make(chan struct{})
		go func() {
			beginDrain(&health, 6*time.Second) // drainDelay is a third of the shutdown deadline: two seconds
			close(drained)
		}()
		synctest.Wait()
		if health.Ready() {
			t.Fatal("still ready while draining")
		}
		select {
		case <-drained:
			t.Fatal("returned before the edge could stop routing here")
		default:
		}
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case <-drained:
			t.Fatal("returned before the edge delay ended")
		default:
		}
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case <-drained:
		default:
			t.Fatal("still waiting after the edge delay")
		}
	})
	synctest.Test(t, func(t *testing.T) {
		var health httpserver.Health
		drained := make(chan struct{})
		go func() {
			beginDrain(&health, time.Hour)
			close(drained)
		}()
		synctest.Wait()
		select {
		case <-drained:
		default:
			t.Fatal("waited with no traffic to wait for")
		}
	})
}

// TestShutdownEndsRequestsPastTheLimit: a request still holding a database connection when the limit runs out is
// ended, so the pool closes and the node stops within about the limit instead of waiting for the request.
func TestShutdownEndsRequestsPastTheLimit(t *testing.T) {
	t.Parallel()
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
	servers := []*listening{{"AXIOM_PUBLIC_ADDR", "127.0.0.1:0", httpserver.New(stuck, slog.New(slog.DiscardHandler)), nil, false}}
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
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = shutdown(ctx, servers)
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
		{"AXIOM_PUBLIC_ADDR", free, httpserver.New(http.NotFoundHandler(), logger), nil, false},
		{"AXIOM_PROBES_ADDR", taken.Addr().String(), httpserver.New(http.NotFoundHandler(), logger), nil, false},
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
