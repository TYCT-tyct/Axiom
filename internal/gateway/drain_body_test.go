package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
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

// TestDrainSettlesABodyThatArrivesAfterTheAbort: a request that passed the gate before the drain and is still sending
// its body when the attempts are cancelled must keep the drain open. Its call starts no attempt (408) but its
// reservation is settled before the deadline, rather than the drain ending the settlements at the abort because the
// executor was empty then.
func TestDrainSettlesABodyThatArrivesAfterTheAbort(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	a := r.funded(1e12)
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
	<-body.started

	out := r.handler.Outstanding
	drainCtx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		out.Drain(drainCtx, time.Minute) // the abort is now: the whole deadline is left for the settlements
	}()
	<-out.shutdown.Done()
	if err := out.end.Err(); err != nil {
		t.Fatalf("the settlements' context ended at the abort, with a request still reading its body: %v", err)
	}
	close(body.release)
	<-served
	<-drained
	if !strings.Contains(rec.Body.String(), `"timeout"`) {
		t.Errorf("the call admitted after the abort answered %d: %s", rec.Code, rec.Body.String())
	}
	for _, e := range up.eps {
		if e.sends != 0 {
			t.Errorf("an attempt was made after the abort")
		}
	}
	if n := r.count(pendingCount); n != 0 {
		t.Errorf("%d reservations left pending for the cleanup; the drain ended before the settlement", n)
	}
	r.ledgerBalanced()
}

// TestDrainWaitsForWorkHandedOffAsTheRequestEnds: a request hands a settlement to the background and returns while the
// drain is checking. The settlement is counted before the request's count is released, so the count never reads zero
// in between, and the drain neither returns nor ends the settlements' context until it is done. Registering the
// hand-off inside its goroutine instead lets the drain see zero in that gap; the repetition makes that race show.
func TestDrainWaitsForWorkHandedOffAsTheRequestEnds(t *testing.T) {
	t.Parallel()
	for range 100 {
		synctest.Test(t, func(t *testing.T) {
			out, start := NewOutstanding(), time.Now()
			request := out.add() // past the gate
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			drained := make(chan struct{})
			go func() {
				defer close(drained)
				out.Drain(ctx, 10*time.Second)
			}()
			synctest.Wait() // the drain is waiting on the request
			settled := make(chan struct{})
			out.Go(func() { <-settled }) // the settlement the request could not write in time
			request()
			synctest.Wait()
			select {
			case <-drained:
				t.Fatal("the drain returned between the request and the settlement it handed off")
			default:
			}
			if err := out.end.Err(); err != nil {
				t.Fatalf("the settlements' context ended before the settlement: %v", err)
			}
			close(settled)
			<-drained
			if waited := time.Since(start); waited != 0 {
				t.Errorf("the drain returned after %v instead of when the count reached zero", waited)
			}
		})
	}
}
