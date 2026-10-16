package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/suppliers"
)

// The metering tests reserve by segment: 200 output tokens a segment, extended at 40%–60% of one (80 to 120 tokens),
// with an allowance of a second at 100 tokens a second, and 1000 tokens at most. Text counts a token a byte, and so do
// the raw bytes of an event not yet read whole: the tests send one event at a time.

// text is a Messages event that delivers text of n bytes.
func text(n int) string {
	return `event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "` + strings.Repeat("x", n) + `"}}

`
}

func segmented(o options) options {
	o.segmentable = true
	o.policy = suppliers.CallPolicy{Streams: true, Cancels: true}
	return o
}

// TestAStreamExtendsItsReservationAsItGoes: every segment is asked for before the last is spent, numbered on, each to
// a larger target, and the stream is never held up.
func TestAStreamExtendsItsReservationAsItGoes(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, segmented(options{}))
		beats := []beat{h.feed(evStart), h.feed(evBlock)}
		for range 9 {
			beats = append(beats, beat{wait: time.Second, data: text(100)})
		}
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(append(beats, h.feed(evBlockStop, evMsgDelta, evMsgStop))...)}
		h.run()
		if h.elapsed() != 9*time.Second || !strings.Contains(h.w.body(), "message_stop") {
			t.Fatalf("took %v:\n%s", h.elapsed(), h.w.body())
		}
		checkExtensions(t, h)
		if a := h.ended().outcome.Attempts[0]; a.Result != billing.Committed || a.Finish != billing.FinishStop {
			t.Errorf("attempt %+v", a)
		}
	})
}

func checkExtensions(t *testing.T, h *harness) {
	t.Helper()
	h.mu.Lock()
	asked := h.extensions
	h.mu.Unlock()
	if len(asked) < 3 {
		t.Fatalf("%d extensions for 900 tokens in segments of 200", len(asked))
	}
	for i, r := range asked {
		if r.Seq != i+2 || r.Entry != 0 || i > 0 && r.Target.Cmp(asked[i-1].Target) <= 0 {
			t.Errorf("extension %d is %+v after %+v", i, r, asked[max(i-1, 0)])
		}
	}
}

// TestASpentSegmentStopsTheReadUntilItsExtensionComes: the event that spends the segment is written only once the next
// is approved, and the wait does not count as the upstream being idle.
func TestASpentSegmentStopsTheReadUntilItsExtensionComes(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, segmented(options{
			config: func(c *Config) { c.IdleTimeout, c.KeepAlive = 500*time.Millisecond, 100*time.Millisecond },
			extend: func(context.Context, billing.ExtendRequest) error {
				time.Sleep(900 * time.Millisecond)
				return nil
			},
		}))
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(evStart), h.feed(evBlock), beat{data: text(250)}, h.feed(evBlockStop, evMsgDelta, evMsgStop))}
		h.run()
		if !strings.Contains(h.w.body(), "message_stop") || h.ended().outcome.Attempts[0].Finish != billing.FinishStop {
			t.Fatalf("the stream did not end normally:\n%s", h.w.body())
		}
		h.w.mu.Lock()
		first := h.w.writes[0]
		h.w.mu.Unlock()
		if got := first.at.Sub(h.started); got != 900*time.Millisecond || !strings.Contains(first.data, strings.Repeat("x", 250)) {
			t.Errorf("the event that spent the segment was written after %v: %q", got, first.data)
		}
	})
}

// cutCase is an extension that fails, and how the call ends for it.
type cutCase struct {
	name        string
	extend      func(context.Context, billing.ExtendRequest) error
	committed   bool // the content began before the segment ran out
	code, limit string
	status      int
	retryAfter  string
	took        time.Duration // from the segment running out to the cut
}

// TestARefusedExtensionCutsTheStream: after the commit point the stream ends where it is with an error event of the
// format, credits_insufficient naming the source or billing_unavailable, and what spent the segment is not written;
// before it, nothing was delivered: the call is answered with the error, no other entry is tried, and billing is told
// the attempt delivered nothing, which charges no model usage. The upstream is stopped either way.
func TestARefusedExtensionCutsTheStream(t *testing.T) {
	slow := func(ctx context.Context, _ billing.ExtendRequest) error {
		<-ctx.Done()
		return ctx.Err()
	}
	refuse := func(err error) func(context.Context, billing.ExtendRequest) error {
		return func(context.Context, billing.ExtendRequest) error { return err }
	}
	db := errors.New("connection refused")
	for _, tc := range []cutCase{
		{"a key limit", refuse(&billing.Insufficient{Source: billing.SourceKey}), true, "credits_insufficient", "key_limit", 200, "", 0},
		{"a holder reaped", refuse(billing.ErrTerminated), true, "billing_unavailable", "", 200, "", 0},
		{"the database failing", refuse(db), true, "billing_unavailable", "", 200, "", 0},
		{"the database too slow", slow, true, "billing_unavailable", "", 200, "", time.Second},
		{"the balance, before the commit point", refuse(&billing.Insufficient{Source: billing.SourceBalance}), false, "credits_insufficient", "balance", 402, "", 0},
		{"a limit that resets, before the commit point", refuse(&billing.Insufficient{Source: billing.SourceWorkspace, Transient: true}), false, "credits_insufficient", "workspace_budget", 402, "60", 0},
		{"the database, before the commit point", refuse(db), false, "billing_unavailable", "", 503, "", 0},
	} {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, segmented(options{entries: 2, extend: tc.extend}))
			beats := []beat{h.feed(evStart), h.feed(evBlock), {data: text(250)}}
			if tc.committed {
				beats = []beat{h.feed(evStart), h.feed(evBlock), h.feed(evHello), {wait: time.Second, data: text(250)}}
			}
			h.up.eps[uuidOf(ep(1))].script = []scripted{hold(beats...)} // open until the attempt is cancelled
			h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
			h.run()
			checkCut(t, h, tc)
		})
	}
}

func checkCut(t *testing.T, h *harness, tc cutCase) {
	t.Helper()
	body := h.w.body()
	limit := tc.limit == "" || strings.Contains(body, `"limit_source":"`+tc.limit+`"`)
	if !strings.Contains(body, `"error_type":"`+tc.code+`"`) || !limit || strings.Contains(body, strings.Repeat("x", 250)) {
		t.Errorf("%s: the client got:\n%s", tc.name, body)
	}
	if h.w.status != tc.status || h.w.header.Get("Retry-After") != tc.retryAfter || h.up.endpoint(2).sends != 0 {
		t.Errorf("%s: status %d, Retry-After %q, the next entry sent %d", tc.name, h.w.status, h.w.header.Get("Retry-After"), h.up.endpoint(2).sends)
	}
	want := billing.FailedBeforeCommit
	if tc.committed {
		want = billing.Committed
		checkCutStream(t, h, tc)
	}
	if a := h.ended().outcome.Attempts; len(a) != 1 || a[0].Result != want || a[0].Finish != billing.FinishTruncated {
		t.Errorf("%s: attempts %+v", tc.name, a)
	}
}

// checkCutStream: a stream cut after the commit point ends with the format's error event, when the extension failed.
func checkCutStream(t *testing.T, h *harness, tc cutCase) {
	t.Helper()
	body := h.w.body()
	if !strings.HasPrefix(body[strings.LastIndex(body, "event: "):], "event: error\n") {
		t.Errorf("%s: the stream does not end with an error event:\n%s", tc.name, body)
	}
	if got := h.elapsed() - time.Second; got != tc.took { // the segment ran out a second in
		t.Errorf("%s: cut %v after the segment ran out, want %v", tc.name, got, tc.took)
	}
}

// TestNoSettlementWhileAnExtensionIsInFlight: billing settles a reservation with no extension in flight, so a stream that
// breaks while one is waits for it first.
func TestNoSettlementWhileAnExtensionIsInFlight(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, segmented(options{extend: func(context.Context, billing.ExtendRequest) error {
			time.Sleep(800 * time.Millisecond)
			return nil
		}}))
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(evStart), h.feed(evBlock), beat{data: text(130)}, beat{hangup: true})}
		h.run()
		h.mu.Lock()
		asked := len(h.extensions)
		h.mu.Unlock()
		if asked != 1 {
			t.Fatalf("%d extensions, want the one past the extension point", asked)
		}
		if s := h.ended(); s.at.Sub(h.started) != 800*time.Millisecond || s.outcome.Attempts[0].Finish != billing.FinishError {
			t.Errorf("settled after %v, %+v", s.at.Sub(h.started), s.outcome.Attempts[0])
		}
	})
}

// TestAWriteMayBlockOnlyAsLongAsTheSegmentLasts: the write deadline is the time the approved output takes at the
// model's fastest, 195 tokens at 100 a second after the first 5, so a client that does not read is gone by then.
func TestAWriteMayBlockOnlyAsLongAsTheSegmentLasts(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, segmented(options{}))
		h.w.stall = true
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold(h.feed(evStart), h.feed(evBlock), h.feed(evHello))}
		h.run()
		if got := h.elapsed(); got != 1950*time.Millisecond {
			t.Errorf("a client that does not read was given %v", got)
		}
		if s := h.ended(); !s.outcome.Cancelled {
			t.Errorf("outcome %+v", s.outcome)
		}
	})
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, segmented(options{}))
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(evStart), h.feed(evBlock), h.feed(evHello), h.feed(evBang), h.feed(evBlockStop, evMsgDelta, evMsgStop))}
		h.run()
		if h.w.status != http.StatusOK || h.w.deadline.Sub(h.started) >= h.cfg.WriteTimeout {
			t.Errorf("status %d, the last deadline %v after the start", h.w.status, h.w.deadline.Sub(h.started))
		}
	})
}
