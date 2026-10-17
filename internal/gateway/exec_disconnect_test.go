package gateway

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// The four things that can happen when the client goes (docs/domains/gateway.md「用量与费用」), and what a write that does
// not go through is: nothing the client does moves money directly; the call is settled, as cancelled, with what its
// attempt had used.

// TestAClientThatGoesBeforeAnythingWasSentCostsNothing: the reservation is released, by a settlement without attempts, and
// nothing is written.
func TestAClientThatGoesBeforeAnythingWasSentCostsNothing(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		h.client()
		h.run()
		s := h.ended()
		if len(s.ending.Attempts) != 0 || !s.ending.Cancelled || h.up.endpoint(1).acquired != 0 || h.w.wrote() != 0 || h.w.status != 0 {
			t.Errorf("ending %+v, %d acquired, %d writes, status %d", s.ending, h.up.endpoint(1).acquired, h.w.wrote(), h.w.status)
		}
	})
}

// TestAClientThatGoesFromAnEndpointThatPassesCancellationOn: the upstream is cancelled at once, whatever it was still to
// send, and the usage so far is settled as cancelled.
func TestAClientThatGoesFromAnEndpointThatPassesCancellationOn(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{policy: suppliers.CallPolicy{Streams: true, Cancels: true}})
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold(h.feed(evStart, evBlock, evHello), h.after(time.Hour, evBang))}
		done := h.start()
		time.Sleep(2 * time.Second)
		h.client()
		<-done
		h.check()
		if got := h.elapsed(); got != 2*time.Second {
			t.Errorf("the call went on for %v after the client left", got-2*time.Second)
		}
		s := h.ended()
		a := s.ending.Attempts[0]
		if !s.ending.Cancelled || a.Result != billing.Committed || a.Finish != billing.FinishCancelled || len(a.Usage.Segments) != 1 {
			t.Errorf("ending %+v", s.ending)
		}
		if strings.Contains(h.w.body(), "event: error") || h.w.wrote() != 1 {
			t.Errorf("something was written to a client that had gone (%d writes):\n%s", h.w.wrote(), h.w.body())
		}
	})
}

// TestAClientThatGoesFromAnEndpointThatDoesNot: the upstream is read to its end, for it bills the whole of what it
// generates, but nothing more is forwarded; the settlement waits for the end and has all its usage, as reported.
func TestAClientThatGoesFromAnEndpointThatDoesNot(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(
			h.feed(evStart, evBlock, evHello), h.after(5*time.Second, evBang), h.after(40*time.Second, evBlockStop, evMsgDelta, evMsgStop),
		)}
		done := h.start()
		time.Sleep(2 * time.Second)
		h.client()
		<-done
		h.check()
		if got := h.elapsed(); got != 45*time.Second {
			t.Errorf("the call took %v, the upstream took 45s", got)
		}
		s := h.ended()
		a := s.ending.Attempts[0]
		if !s.ending.Cancelled || a.Result != billing.Committed || a.Usage.Segments[0].Usage.OutputTotal() != 15 {
			t.Errorf("ending %+v", s.ending)
		}
		if h.w.wrote() != 1 || strings.Contains(s.body, "message_stop") {
			t.Errorf("%d writes: a client that had gone was written to:\n%s", h.w.wrote(), h.w.body())
		}
	})
}

// TestAClientThatGoesBeforeTheFirstContentOfAnEndpointThatDoesNotCancel: nothing was delivered, so the attempt is no commit
// (the journal never says so), and it is read to the end for its usage, not written.
func TestAClientThatGoesBeforeTheFirstContentOfAnEndpointThatDoesNotCancel(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(evStart), h.after(10*time.Second, evBlock, evHello, evBang, evBlockStop, evMsgDelta, evMsgStop))}
		done := h.start()
		time.Sleep(time.Second)
		h.client()
		<-done
		h.check()
		a := h.ended().ending.Attempts[0]
		if h.w.status != 0 || h.w.wrote() != 0 || a.Result != billing.FailedBeforeCommit || a.Usage.Segments[0].Usage.OutputTotal() != 15 {
			t.Errorf("status %d, %d writes, attempt %+v", h.w.status, h.w.wrote(), a)
		}
		for _, e := range h.up.journal {
			if e.Committed {
				t.Error("the journal says content was handed to a client that was gone")
			}
		}
	})
}

// TestAnEndpointThatDoesNotCancelIsStillStoppedByTheIdleTimeout: the read goes on for the usage only as long as the upstream
// keeps saying something; the settlement then is of what had come.
func TestAnEndpointThatDoesNotCancelIsStillStoppedByTheIdleTimeout(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold(h.feed(evStart, evBlock, evHello))}
		done := h.start()
		time.Sleep(time.Second)
		h.client()
		<-done
		h.check()
		if got := h.elapsed(); got != h.cfg.IdleTimeout { // counted from the last thing the upstream said
			t.Errorf("the call took %v", got)
		}
		if s := h.ended(); !s.ending.Cancelled || s.ending.Attempts[0].Finish != billing.FinishCancelled {
			t.Errorf("ending %+v", s.ending)
		}
	})
}

// TestAWriteThatFailsMeansTheClientIsGone: nothing is written after it; an endpoint that passes cancellation on is
// cancelled then, one that does not is read on.
func TestAWriteThatFailsMeansTheClientIsGone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cancels bool
		took    time.Duration
	}{
		{"cancels", true, time.Second},
		{"does not", false, 31 * time.Second},
	} {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, options{policy: suppliers.CallPolicy{Streams: true, Cancels: tc.cancels}})
			h.w.failFrom = 2
			h.up.eps[uuidOf(ep(1))].script = []scripted{serve(
				h.feed(evStart, evBlock, evHello), h.after(time.Second, evBang), h.after(30*time.Second, evBlockStop, evMsgDelta, evMsgStop),
			)}
			h.run()
			if h.w.wrote() != 1 || h.elapsed() != tc.took {
				t.Errorf("%s: %d writes, took %v", tc.name, h.w.wrote(), h.elapsed())
			}
			if !h.ended().ending.Cancelled {
				t.Errorf("%s: a client that does not read was settled as one that stayed", tc.name)
			}
		})
	}
}

// TestAClientThatDoesNotTakeAWriteInTimeIsGone: every write has a deadline, the write timeout from the moment it starts;
// one that is not taken by then is a client that has gone.
func TestAClientThatDoesNotTakeAWriteInTimeIsGone(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{policy: suppliers.CallPolicy{Streams: true, Cancels: true}})
		h.w.stall = true
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold(h.feed(evStart, evBlock, evHello))}
		h.run()
		if got, want := h.w.deadline, h.started.Add(h.cfg.WriteTimeout); !got.Equal(want) {
			t.Errorf("the write deadline is %v, want %v", got, want)
		}
		if got := h.elapsed(); got != h.cfg.WriteTimeout {
			t.Errorf("the call took %v", got)
		}
		if s := h.ended(); !s.ending.Cancelled || s.ending.Attempts[0].Result != billing.FailedBeforeCommit {
			t.Errorf("ending %+v", s.ending)
		}
	})
}

// TestSettlingFails: a stream that completed ends without a cost and says the billing is pending; a stream that broke
// ends with the error and no cost, as the settlement could not say; and a settlement that has a charge to report puts it
// in the error that answers a call that sent nothing.
func TestSettlingFails(t *testing.T) {
	usd, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("completed", func(t *testing.T) {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, options{settleErr: io.ErrClosedPipe})
			h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
			h.run()
			body := h.w.body()
			if !strings.Contains(body, `"billing_pending":true`) || strings.Contains(body, `"cost"`) || !strings.Contains(body, "message_stop") {
				t.Errorf("the stream:\n%s", body)
			}
		})
	})
	t.Run("broken", func(t *testing.T) {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, options{settleErr: io.ErrClosedPipe})
			h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(evStart, evBlock, evHello), beat{hangup: true})}
			h.run()
			if body := h.w.body(); !strings.Contains(body, "event: error") || strings.Contains(body, `"cost"`) {
				t.Errorf("the stream:\n%s", body)
			}
		})
	})
	t.Run("charged but sent nothing", func(t *testing.T) {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, options{final: &protocol.Final{Cost: platform.AmountExact(5), Currency: usd}})
			h.run()
			if body := h.w.body(); h.w.status != http.StatusBadGateway || !strings.Contains(body, `"axiom"`) || !strings.Contains(body, `"cost":`) {
				t.Errorf("status %d:\n%s", h.w.status, body)
			}
		})
	})
}

// TestEveryWayOutSettlesOnce, even a panic: the attempt in progress is settled as one whose outcome is unknown, its
// credential and the node's memory given back, and the panic goes on.
func TestEveryWayOutSettlesOnce(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		h.up.eps[uuidOf(ep(1))].script = []scripted{func(context.Context) (*http.Response, *suppliers.Failure) { panic("a bug") }}
		panicked := make(chan any)
		go func() {
			defer func() { panicked <- recover() }()
			h.exec.Run(h.w, h.req, h.call)
		}()
		if p := <-panicked; p != "a bug" {
			t.Fatalf("recovered %v", p)
		}
		h.check()
		if a := h.ended().ending.Attempts; len(a) != 1 || a[0].Result != billing.Unknown {
			t.Errorf("attempts %+v", a)
		}
	})
}

// TestKeepAlivesAndWritesNeverInterleave: with a keep-alive timer that fires as often as events come, every write the
// client sees is one whole event or one whole keep-alive (the race detector watches the rest).
func TestKeepAlivesAndWritesNeverInterleave(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{config: func(c *Config) { c.KeepAlive = time.Millisecond }})
		beats := []beat{h.feed(evStart, evBlock, evHello)}
		for range 300 {
			beats = append(beats, h.after(time.Millisecond, evBang))
		}
		beats = append(beats, h.after(time.Millisecond, evBlockStop, evMsgDelta, evMsgStop))
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(beats...)}
		h.run()
		keep := string(protocol.KeepAlive(protocol.Messages))
		keeps := 0
		for _, w := range h.w.writes {
			switch {
			case w.data == keep:
				keeps++
			case !strings.HasPrefix(w.data, "event: ") || !strings.HasSuffix(w.data, "\n\n") || strings.Contains(w.data, keep):
				t.Fatalf("a write that is not whole events:\n%q", w.data)
			}
		}
		if last := h.w.writes[len(h.w.writes)-1].data; !strings.Contains(last, "message_stop") {
			t.Errorf("a keep-alive after the ending, or no ending:\n%q", last)
		}
		_ = keeps
	})
}

// TestAClientThatLeavesARealConnection: the disconnect on a real connection reaches the executor as the request's
// context ending, and the call is settled as cancelled, its upstream cancelled. The test waits for events, never for
// time.
func TestAClientThatLeavesARealConnection(t *testing.T) {
	h := newHarness(t, t.Context(), options{policy: suppliers.CallPolicy{Streams: true, Cancels: true}})
	h.up.eps[uuidOf(ep(1))].script = []scripted{hold(h.feed(evStart, evBlock, evHello))}
	over := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(over)
		h.exec.Run(w, r, h.call)
	}))
	defer srv.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(resp.Body)
	for lines.Scan() && !strings.Contains(lines.Text(), "Hello") {
	}
	if err := lines.Err(); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, read %v", resp.StatusCode, err)
	}
	_ = resp.Body.Close() //nolint:errcheck // leaving
	<-over
	h.check()
	s := h.ended()
	if a := s.ending.Attempts[0]; !s.ending.Cancelled || a.Result != billing.Committed || a.Finish != billing.FinishCancelled {
		t.Errorf("ending %+v", s.ending)
	}
}
