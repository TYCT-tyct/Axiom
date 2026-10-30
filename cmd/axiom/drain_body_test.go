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

// TestDrainKeepsACallThatWasStillReadingItsBody runs the real call node's handler and executor drains together. The
// request has passed the gate but has not reached admission when the drain starts; it must still run and settle before
// the shared deadline rather than being refused because the executor was momentarily empty.
func TestDrainKeepsACallThatWasStillReadingItsBody(t *testing.T) {
	t.Parallel()
	w := newCallWorld(t)
	w.seedFailover()
	w.price()
	n, _, _ := w.controlledCallPath(&testClock{now: time.Now()})

	body := &blockingBody{started: make(chan struct{}), release: make(chan struct{}), body: strings.NewReader(messagesCall)}
	req := httptest.NewRequestWithContext(w.t.Context(), http.MethodPost, "http://api.main.example/v1/messages", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+w.richKey)
	rec := httptest.NewRecorder()
	served := make(chan struct{})
	go func() {
		defer close(served)
		httpserver.Public(n.handler, slog.New(slog.DiscardHandler), time.Minute).ServeHTTP(rec, req)
	}()
	select {
	case <-body.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not begin reading its body")
	}

	drainCtx, cancel := context.WithTimeout(w.t.Context(), 10*time.Second)
	defer cancel()
	drained := make(chan struct{})
	go func() {
		n.Drain(drainCtx)
		close(drained)
	}()
	select {
	case <-drained:
		t.Fatal("the drain returned while a request that had passed the gate was still reading its body")
	case <-time.After(100 * time.Millisecond):
	}
	close(body.release)
	select {
	case <-drained:
	case <-time.After(10 * time.Second):
		t.Fatal("the drain did not finish after the request sent its body")
	}
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("the request did not finish")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("the request answered %d: %s", rec.Code, rec.Body.String())
	}
	if got := len(w.supplier.sent()); got != 1 {
		t.Errorf("the upstream saw %d attempts, want 1", got)
	}
}
