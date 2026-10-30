package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"axiom/internal/gateway"
)

// TestIngressLimitsAreCompatibleWithTheCore: the ingress waits longer than the core's own limits, so a normal call is
// never cut by the proxy, while a stopped one still ends. The write limit is the core's own per-write limit.
func TestIngressLimitsAreCompatibleWithTheCore(t *testing.T) {
	gate := gateway.DefaultConfig()
	if ingressHeaderLimit <= gate.CompactHeaderWait {
		t.Fatalf("ingress header limit %v does not cover the core's %v", ingressHeaderLimit, gate.CompactHeaderWait)
	}
	if ingressIdleLimit <= gate.IdleTimeout {
		t.Fatalf("ingress idle limit %v does not cover the core's %v", ingressIdleLimit, gate.IdleTimeout)
	}
	if ingressRequestLimit <= gate.MaxCall {
		t.Fatalf("ingress request limit %v does not cover the core's %v", ingressRequestLimit, gate.MaxCall)
	}
	if ingressWriteLimit != gate.WriteTimeout {
		t.Fatalf("ingress write limit %v differs from the core's %v", ingressWriteLimit, gate.WriteTimeout)
	}
	if got := ingressTransport(nil).ResponseHeaderTimeout; got != ingressHeaderLimit {
		t.Fatalf("transport header limit = %v, want %v", got, ingressHeaderLimit)
	}
}

// TestIngressWriteDeadlineEndsAStoppedClient: a client that stops reading blocks Write, which the request context
// cannot interrupt; the write deadline makes the handler return, and the handler clears the deadline afterwards.
func TestIngressWriteDeadlineEndsAStoppedClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		writer := newStalledWriter()
		handler := ingressHandlerWith(testCoreURL(t, "http://core.example"),
			staticTransport{body: io.NopCloser(strings.NewReader("payload"))}, slog.New(slog.DiscardHandler), 0, ingressRequestLimit, 0)
		done := make(chan struct{})
		go func() {
			defer close(done)
			handler.ServeHTTP(writer, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
		}()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("the handler returned before the write deadline")
		default:
		}
		time.Sleep(ingressWriteLimit)
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("the handler is still blocked in a write after its deadline")
		}
		select {
		case <-writer.cleared:
		default:
			t.Fatal("the handler did not clear the write deadline")
		}
	})
}

// TestIngressIdleBodyEndsAStalledCore: a core that sends headers and then nothing keeps its body open; the idle body
// timer closes it, and the handler returns without a client-side timeout.
func TestIngressIdleBodyEndsAStalledCore(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		body := &closeBlockingBody{closed: make(chan struct{})}
		handler := ingressHandlerWith(testCoreURL(t, "http://core.example"), staticTransport{body: body},
			slog.New(slog.DiscardHandler), time.Minute, time.Hour, 0)
		done := make(chan struct{})
		go func() {
			defer close(done)
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
		}()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("the handler returned before the idle deadline")
		default:
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("the handler is still reading an idle body")
		}
	})
}

// TestIngressRequestDeadlineEndsAStream: a core that keeps its response open is ended by the whole-request deadline,
// even when it never sends a byte; no client timeout is involved.
func TestIngressRequestDeadlineEndsAStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler := ingressHandlerWith(testCoreURL(t, "http://core.example"), contextTransport{},
			slog.New(slog.DiscardHandler), time.Hour, time.Minute, 0)
		done := make(chan struct{})
		go func() {
			defer close(done)
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
		}()
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("the handler returned before the request deadline")
		default:
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("the handler is still forwarding after the request deadline")
		}
	})
}

// testCoreURL parses the target URL for the ingress handler.
func testCoreURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	core, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return core
}

// staticTransport answers with one fixed body, without a network.
type staticTransport struct {
	body io.ReadCloser
}

func (t staticTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header), Body: t.body, Request: r, ContentLength: 7,
	}, nil
}

// contextTransport answers with a body that ends only when the forwarded request does.
type contextTransport struct{}

func (contextTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header), Body: &contextBody{done: r.Context().Done()}, Request: r,
		ContentLength: 7,
	}, nil
}

type contextBody struct {
	done <-chan struct{}
}

func (b *contextBody) Read([]byte) (int, error) {
	<-b.done
	return 0, context.Canceled
}

func (*contextBody) Close() error { return nil }

// closeBlockingBody blocks in Read until the idle timer closes it.
type closeBlockingBody struct {
	closed chan struct{}
	once   sync.Once
}

func (b *closeBlockingBody) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.EOF
}

func (b *closeBlockingBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

// stalledWriter models a client that accepts headers but never reads the body: Write waits for the deadline the
// handler sets, then fails as a socket write would. A missing deadline blocks forever, which synctest reports.
type stalledWriter struct {
	header   http.Header
	cleared  chan struct{}
	mu       sync.Mutex
	deadline time.Time
}

func newStalledWriter() *stalledWriter {
	return &stalledWriter{header: make(http.Header), cleared: make(chan struct{})}
}

func (w *stalledWriter) Header() http.Header { return w.header }

func (*stalledWriter) WriteHeader(int) {}

func (w *stalledWriter) Write([]byte) (int, error) {
	w.mu.Lock()
	deadline := w.deadline
	w.mu.Unlock()
	if deadline.IsZero() {
		select {} // no deadline: only a deadlock, which synctest turns into a failure
	}
	if wait := time.Until(deadline); wait > 0 {
		time.Sleep(wait)
	}
	return 0, os.ErrDeadlineExceeded
}

func (*stalledWriter) FlushError() error { return nil }

func (w *stalledWriter) SetWriteDeadline(deadline time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if deadline.IsZero() {
		select {
		case <-w.cleared:
		default:
			close(w.cleared)
		}
	}
	w.deadline = deadline
	return nil
}
