package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// slowBody sends its bytes only after the test releases it, so the request can be caught between the gate and the
// executor: the handler has it, the executor does not.
type slowBody struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
	data    []byte
}

func (b *slowBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}

func (*slowBody) Close() error { return nil }

// TestDrainWaitsForARequestStillReadingItsBody: a request that has passed the gate but is still sending its body must
// keep the handler drain open. Before ServeHTTP counted the whole request, the executor was empty and Drain returned,
// then the body arrived and the call outlived the drain with no one left to cancel it.
func TestDrainWaitsForARequestStillReadingItsBody(t *testing.T) {
	r := newRig(t)
	a := r.newAccount(1e9)
	body := &slowBody{started: make(chan struct{}), release: make(chan struct{}), data: []byte(messageBody("acme/beta"))}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", body)
	req.Host = apiHost
	req.Header.Set("Authorization", "Bearer "+a.key)
	rec := httptest.NewRecorder()
	served := make(chan struct{})
	go func() {
		defer close(served)
		r.server.Config.Handler.ServeHTTP(rec, req) // the public listener's handler, without a network in between
	}()
	select {
	case <-body.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not begin reading its body")
	}
	drainCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	drained := make(chan struct{})
	go func() {
		r.handler.Drain(drainCtx, time.Second)
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("drain returned while a request that had passed the gate was still reading its body")
	case <-time.After(100 * time.Millisecond):
	}
	close(body.release)
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("drain did not finish after the request sent its body")
	}
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not finish")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("the request answered %d: %s", rec.Code, rec.Body.String())
	}
}

// pendingGate stands in for the handler's gate: Pending says a request may still hand the executor a call, and Idle is
// closed when that is no longer so.
type pendingGate struct {
	pending atomic.Bool
	idle    chan struct{}
}

func (g *pendingGate) Pending() bool         { return g.pending.Load() }
func (g *pendingGate) Idle() <-chan struct{} { return g.idle }

// TestDrainWithWaitsForARequestThatCanStillReachTheExecutor: the executor must not treat its own momentary emptiness
// as the moment to cancel. The gate holds a request between the handler and the executor; when that request is released
// after the drain began, it must still get its attempt and settlement, up to abort and the shared deadline.
func TestDrainWithWaitsForARequestThatCanStillReachTheExecutor(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		gate := &pendingGate{idle: make(chan struct{})}
		gate.pending.Store(true)
		stop, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		drained := make(chan struct{})
		go func() {
			h.exec.DrainWith(stop, 5*time.Second, gate)
			close(drained)
		}()
		time.Sleep(time.Second) // the drain is waiting on the gate, with the executor empty
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		done := h.start() // the held request reaches the executor, before abort
		<-done
		gate.pending.Store(false)
		close(gate.idle)
		select {
		case <-drained:
		case <-time.After(5 * time.Second):
			t.Fatal("the drain did not finish after the gate became idle")
		}
		if n := h.up.eps[uuidOf(ep(1))].sends; n != 1 {
			t.Errorf("the call made %d attempts; the drain cancelled the executor before the request arrived", n)
		}
		h.check()
	})
}
