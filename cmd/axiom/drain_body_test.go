package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/httpserver"
)

// blockingBody keeps a request between the gate and the executor until the test releases it, the window in which the
// executor used to be empty and cancelled itself before the request could be admitted.
type blockingBody struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
	body    io.Reader
}

func (b *blockingBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.body.Read(p)
}

func (*blockingBody) Close() error { return nil }

// TestDrainKeepsACallThatWasStillReadingItsBody runs the real call node behind the public listener's gate. A request
// that passed the gate before the drain and has not reached admission when it starts must still run and settle before
// the shared deadline, while a request that arrives once the drain began is refused at the gate with a retryable 503.
func TestDrainKeepsACallThatWasStillReadingItsBody(t *testing.T) {
	t.Parallel()
	w := newCallWorld(t)
	w.seedFailover()
	w.price()
	n, _, _ := w.controlledCallPath(&testClock{now: time.Now()})
	var health httpserver.Health
	public := httpserver.Public(health.Gate(n.handler), slog.New(slog.DiscardHandler), time.Minute)
	call := func(body io.Reader) *http.Request {
		req := httptest.NewRequestWithContext(w.t.Context(), http.MethodPost, "http://api.main.example/v1/messages", body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+w.richKey)
		return req
	}

	body := &blockingBody{started: make(chan struct{}), release: make(chan struct{}), body: strings.NewReader(messagesCall)}
	rec := httptest.NewRecorder()
	served := make(chan struct{})
	go func() {
		defer close(served)
		public.ServeHTTP(rec, call(body))
	}()
	<-body.started

	health.BeginDrain()
	late := httptest.NewRecorder()
	public.ServeHTTP(late, call(strings.NewReader(messagesCall)))
	if late.Code != http.StatusServiceUnavailable || late.Header().Get("Retry-After") != "1" {
		t.Errorf("a request after the drain began answered %d %v", late.Code, late.Header())
	}
	drainCtx, cancel := context.WithTimeout(w.t.Context(), time.Minute)
	defer cancel()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		n.Drain(drainCtx)
	}()
	close(body.release)
	<-served
	<-drained
	if rec.Code != http.StatusOK {
		t.Fatalf("the request answered %d: %s", rec.Code, rec.Body.String())
	}
	if got := len(w.supplier.sent()); got != 1 {
		t.Errorf("the upstream saw %d attempts, want the one call's", got)
	}
}
