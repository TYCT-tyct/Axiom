package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
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
