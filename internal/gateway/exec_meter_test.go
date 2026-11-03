package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
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
	t.Parallel()
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

// TestASpentSegmentStopsTheReadUntilItsExtensionComes: the event that spends the segment, past its end or to its last
// token, is written only once the next is approved, and the wait does not count as the upstream being idle.
func TestASpentSegmentStopsTheReadUntilItsExtensionComes(t *testing.T) {
	t.Parallel()
	for _, n := range []int{250, 200} {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, segmented(options{
				config: func(c *Config) { c.IdleTimeout, c.KeepAlive = 500*time.Millisecond, 100*time.Millisecond },
				extend: func(context.Context, billing.ExtendRequest) error {
					time.Sleep(900 * time.Millisecond)
					return nil
				},
			}))
			h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(evStart), h.feed(evBlock), beat{data: text(n)}, h.feed(evBlockStop, evMsgDelta, evMsgStop))}
			h.run()
			if !strings.Contains(h.w.body(), "message_stop") || h.ended().outcome.Attempts[0].Finish != billing.FinishStop {
				t.Fatalf("%d bytes: the stream did not end normally:\n%s", n, h.w.body())
			}
			h.w.mu.Lock()
			first := h.w.writes[0]
			h.w.mu.Unlock()
			if got := first.at.Sub(h.started); got != 900*time.Millisecond || !strings.Contains(first.data, strings.Repeat("x", n)) {
				t.Errorf("%d bytes: the event that spent the segment was written after %v", n, got)
			}
		})
	}
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
	t.Parallel()
	slow := func(ctx context.Context, _ billing.ExtendRequest) error {
		<-ctx.Done()
		return ctx.Err()
	}
	refuse := func(err error) func(context.Context, billing.ExtendRequest) error {
		return func(context.Context, billing.ExtendRequest) error { return err }
	}
	db := errors.New("connection refused")
	for _, tc := range []cutCase{
		{"a key limit", refuse(&billing.Insufficient{Source: billing.SourceKey}), true, "credits_insufficient", "openrouter_key_limit", 200, "", 0},
		{"a holder reaped", refuse(billing.ErrTerminated), true, "billing_unavailable", "", 200, "", 0},
		{"the database failing", refuse(db), true, "billing_unavailable", "", 200, "", 0},
		{"the database too slow", slow, true, "billing_unavailable", "", 200, "", time.Second},
		{"the balance, before the commit point", refuse(&billing.Insufficient{Source: billing.SourceBalance}), false, "credits_insufficient", "balance", 402, "", 0},
		{"a limit that resets, before the commit point", refuse(&billing.Insufficient{Source: billing.SourceWorkspace, Transient: true}), false, "credits_insufficient", "openrouter_workspace_budget", 403, "60", 0},
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

//nolint:cyclop // one assertion per property of the cut, kept together
func checkCut(t *testing.T, h *harness, tc cutCase) {
	t.Helper()
	body := h.w.body()
	limit := tc.limit == "" || strings.Contains(body, `"limit_source":"`+tc.limit+`"`)
	typ := "api_error"
	if tc.code == "credits_insufficient" {
		typ = "billing_error"
	}
	if !strings.Contains(body, `"type":"`+typ+`"`) || !strings.Contains(body, `"error_type":"`+tc.code+`"`) ||
		!limit || strings.Contains(body, strings.Repeat("x", 250)) {
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
	h.w.mu.Lock()
	last := h.w.writes[len(h.w.writes)-1]
	h.w.mu.Unlock()
	if got := last.deadline.Sub(last.at); got != h.cfg.WriteTimeout { // not the nothing a spent segment leaves
		t.Errorf("%s: the error event had %v to be written", tc.name, got)
	}
}

// TestAnExtensionThatFailsEarlyCutsAtOnce: one that fails while the segment still has room cuts the stream when it
// fails, whether the upstream is quiet by then or goes on sending, without waiting for the segment to run out
// (docs/domains/billing.md「预留与结算」) or for the idle timeout; unless the upstream has already ended normally, when
// there is nothing left to stop.
func TestAnExtensionThatFailsEarlyCutsAtOnce(t *testing.T) {
	t.Parallel()
	failing := func(context.Context, billing.ExtendRequest) error { return errors.New("connection refused") }
	run(t, func(t *testing.T, ctx context.Context) {
		late := func(context.Context, billing.ExtendRequest) error {
			time.Sleep(time.Second) // the failure comes after the upstream's end, and is taken with it
			return errors.New("connection refused")
		}
		h := newHarness(t, ctx, segmented(options{extend: late}))
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(evStart), h.feed(evBlock), beat{data: text(130)},
			h.feed(evBlockStop), h.feed(evMsgDelta), h.feed(evMsgStop))}
		h.run()
		if body := h.w.body(); strings.Contains(body, "event: error") || !strings.Contains(body, "message_stop") ||
			h.ended().outcome.Attempts[0].Finish != billing.FinishStop {
			t.Errorf("a stream that ended was cut:\n%s", body)
		}
	})
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, segmented(options{extend: failing}))
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold(h.feed(evStart), h.feed(evBlock), beat{data: text(130)}, beat{wait: time.Second, data: text(1)})}
		h.run()
		if h.elapsed() != 0 || !strings.Contains(h.w.body(), `"error_type":"billing_unavailable"`) || strings.Contains(h.w.body(), `"x"`) {
			t.Errorf("after %v:\n%s", h.elapsed(), h.w.body())
		}
	})
}

// TestAnExtensionThatFailsCutsAnUpstreamThatSaysNothing: the owner is blocked in a read of an upstream that has gone
// quiet when the extension fails; the failure ends the read and the call, billing_unavailable, not the idle timeout
// three minutes on, which would call it the upstream's.
func TestAnExtensionThatFailsCutsAnUpstreamThatSaysNothing(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, segmented(options{extend: func(context.Context, billing.ExtendRequest) error {
			return errors.New("connection refused")
		}}))
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold(h.feed(evStart), h.feed(evBlock), beat{data: text(130)})}
		h.run()
		body := h.w.body()
		if got := h.elapsed(); got != 0 || !strings.Contains(body, `"error_type":"billing_unavailable"`) || strings.Contains(body, `"error_type":"timeout"`) {
			t.Errorf("cut after %v:\n%s", got, body)
		}
		if a := h.ended().outcome.Attempts; len(a) != 1 || a[0].Result != billing.Committed || a[0].Finish != billing.FinishTruncated {
			t.Errorf("attempts %+v", a)
		}
	})
}

// freeOutput is a price schedule whose output costs nothing, as a promotion's may.
const freeOutput = `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0"}}`

// TestOutputThatCostsNothingNeverWaitsForAnExtension: it spends no segment, so a stream of several segments' worth is
// read through without an extension, and a client that goes while the upstream is quiet ends the call. A meter that
// said the segment was spent when no extension was coming left the owner waiting for none, and again, with the
// processor to itself and no way to cancel it.
func TestOutputThatCostsNothingNeverWaitsForAnExtension(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, segmented(options{price: freeOutput}))
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(evStart), h.feed(evBlock), beat{data: text(250)}, beat{data: text(250)},
			h.feed(evBlockStop, evMsgDelta, evMsgStop))}
		h.run()
		h.mu.Lock()
		asked := len(h.extensions)
		h.mu.Unlock()
		if !strings.Contains(h.w.body(), "message_stop") || asked != 0 || h.elapsed() != 0 {
			t.Errorf("%d extensions, took %v:\n%s", asked, h.elapsed(), h.w.body())
		}
	})
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, segmented(options{price: freeOutput}))
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold(h.feed(evStart), h.feed(evBlock), beat{data: text(250)})}
		done := h.start()
		time.Sleep(time.Second)
		h.client()
		<-done
		h.check()
		if s := h.ended(); s.outcome.Attempts[0].Finish != billing.FinishCancelled || h.elapsed() != time.Second {
			t.Errorf("finish %v after %v", s.outcome.Attempts[0].Finish, h.elapsed())
		}
	})
}

// TestNoSettlementWhileAnExtensionIsInFlight: billing settles a reservation with no extension in flight, so a stream that
// breaks while one is waits for it first.
func TestNoSettlementWhileAnExtensionIsInFlight(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, segmented(options{}))
		h.w.stall = true
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold(h.feed(evStart), h.feed(evBlock), h.feed(evHello))}
		h.run()
		if got := h.elapsed(); got != 1950*time.Millisecond {
			t.Errorf("a client that does not read was given %v", got)
		}
		if s := h.ended(); s.outcome.Attempts[0].Finish != billing.FinishCancelled {
			t.Errorf("outcome %+v", s.outcome)
		}
	})
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, segmented(options{}))
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(evStart), h.feed(evBlock), h.feed(evHello), h.feed(evBang), h.feed(evBlockStop, evMsgDelta, evMsgStop))}
		h.run()
		h.w.mu.Lock()
		first, last := h.w.writes[0], h.w.writes[len(h.w.writes)-1]
		h.w.mu.Unlock()
		// The content is written within what the segment covers; the ending, once the upstream is read no more, within
		// the write timeout.
		if h.w.status != http.StatusOK || first.deadline.Sub(first.at) != 1950*time.Millisecond || last.deadline.Sub(last.at) != h.cfg.WriteTimeout {
			t.Errorf("status %d, deadlines %v and %v", h.w.status, first.deadline.Sub(first.at), last.deadline.Sub(last.at))
		}
	})
}

// TestATruncatedStreamConsumesNoMoreThanItsAllowance: the extension is in flight and the upstream goes on producing, as
// a real one's socket buffers take what it writes, while the gateway stops reading; when the wait is over the attempt is
// cut, and what the upstream produced beyond the segment it had reserved stays within the overshoot allowance that
// segment's reservation set aside, so the supplier cannot bill more than was reserved for the call
// (docs/domains/billing.md「预留与结算」).
func TestATruncatedStreamConsumesNoMoreThanItsAllowance(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		const covered, allowance = 500, 500 // a segment of 500 tokens, and 5s at 100 tokens a second
		const wait = 5 * time.Second        // how long the extension is waited for
		var asked atomic.Int64              // when the extension was asked for, in Unix nanoseconds
		h := newHarness(t, ctx, segmented(options{
			segments: billing.Segments{Output: covered, Overshoot: 5 * time.Second},
			config:   func(c *Config) { c.ExtendWait = wait },
			extend: func(ctx context.Context, _ billing.ExtendRequest) error {
				asked.Store(time.Now().UnixNano())
				<-ctx.Done() // the ledger is too slow: the wait ends the attempt
				return ctx.Err()
			},
		}))
		var produced atomic.Int64
		prefix := []string{h.feed(evStart).data, h.feed(evBlock).data, h.feed(evHello).data}
		h.up.eps[uuidOf(ep(1))].script = []scripted{streaming(prefix, 10, 100*time.Millisecond, &produced)}
		h.run()
		// The extension was asked for 40%–60% of the segment in, and the wait lasted 5s: the upstream, producing at 100
		// tokens a second throughout, made at most 3 seconds' worth more than the segment covered.
		if extra := produced.Load() - covered; extra < 0 || extra > allowance {
			t.Errorf("the upstream produced %d bytes beyond the %d it was covered for, past the %d allowance", extra, covered, allowance)
		}
		// The point the extension is asked at is drawn at random (billing.Quote), so the cut is measured from when it
		// was asked: it ends the read the moment the wait does.
		if got := time.Since(time.Unix(0, asked.Load())); got != wait {
			t.Errorf("the attempt was cut %v after the extension was asked for, not when the wait for it ended", got)
		}
		body := h.w.body()
		if !strings.Contains(body, `"error_type":"billing_unavailable"`) || !strings.HasPrefix(body[strings.LastIndex(body, "event: "):], "event: error\n") {
			t.Errorf("the stream does not end with the error event of an unavailable ledger:\n%.300s", body)
		}
		if a := h.ended().outcome.Attempts; len(a) != 1 || a[0].Result != billing.Committed || a[0].Finish != billing.FinishTruncated {
			t.Errorf("attempts %+v", a)
		}
	})
}

// toolStart is a Messages event that begins a tool call with no arguments, the shortest a call can be.
func toolStart(id string) string {
	return `event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"` + id + `","name":"f","input":{}}}

`
}

// TestToolCallsExtendTheReservation: the protocol counts 30 tool calls the upstream reported no output for, and the
// meter must see them before the segment runs out; the settlement then charges the framework within an approved
// segment instead of leaving the difference to the platform. Empty arguments keep the byte bound at zero, so only the
// count can ask for the extension.
func TestToolCallsExtendTheReservation(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, segmented(options{mutate: func(c *Call) {
			c.Plan.Entries[0].Target.Model = "claude-sonnet-4-5"
		}}))
		beats := []beat{h.feed(evStart)}
		for i := range 30 {
			beats = append(beats, beat{data: toolStart(fmt.Sprintf("toolu_%d", i))})
		}
		beats = append(beats, beat{hangup: true})
		h.up.eps[uuidOf(ep(1))].script = []scripted{serve(beats...)}
		h.run()
		h.mu.Lock()
		asked := len(h.extensions)
		first := h.extensions[0]
		h.mu.Unlock()
		if asked == 0 {
			t.Fatal("no extension: the tool-call framework was not metered")
		}
		if first.Target.Cmp(h.call.Hold.Amount) <= 0 {
			t.Errorf("the extension target %v does not pass the reservation %v; a balance with nothing to spare would refuse it",
				first.Target, h.call.Hold.Amount)
		}
		a := h.ended().outcome.Attempts[0]
		if len(a.Segments) == 0 || a.Segments[0].Usage.OutputTotal() != 1000 {
			t.Fatalf("the settlement charged %+v, want the output cap 1000", a.Segments)
		}
	})
}
