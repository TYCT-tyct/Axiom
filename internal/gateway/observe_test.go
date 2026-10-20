package gateway

import (
	"context"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// TestEveryAttemptIsObservedOnce: each attempt tells quality how it ended, once, and the table decides what it counts
// as. Endpoint 1 starts from one counted failure (health 0.8), so an attempt the table counts as a success lifts it to
// 0.84, one it counts as a failure drops it to 0.64, and one it does not count leaves it at 0.8; endpoint 2, where the
// call goes next, ends normally.
func TestEveryAttemptIsObservedOnce(t *testing.T) {
	const succeeded, failed, uncounted = 0.84, 0.64, 0.8
	fivexx := failing(routing.ScopeEndpoint, false, suppliers.Failure{Cause: suppliers.UpstreamError{Status: 502}})
	cases := []struct {
		name  string
		o     options
		first func(h *harness) scripted
		leave time.Duration // the client leaves this long into the call, when not zero
		want  float64
	}{
		{"a normal answer", options{}, whole, 0, succeeded},
		{"a 5xx", options{entries: 2}, func(*harness) scripted {
			return func(context.Context) (*http.Response, *suppliers.Failure) { return nil, fivexx }
		}, 0, failed},
		{"a 429", options{entries: 2}, func(*harness) scripted { return throttled() }, 0, uncounted},
		{"no response headers in time", options{entries: 2}, func(*harness) scripted {
			return func(ctx context.Context) (*http.Response, *suppliers.Failure) {
				<-ctx.Done() // as suppliers' transport answers a request cancelled once written
				return nil, failing(routing.ScopeEndpoint, false, suppliers.Failure{Cause: suppliers.Transport{}})
			}
		}, 0, failed},
		{
			"no response headers in time for a long input",
			options{entries: 2, mutate: func(c *Call) { c.Request.TextBytes = 4 * (32<<10 + 1) }},
			func(*harness) scripted {
				return func(ctx context.Context) (*http.Response, *suppliers.Failure) {
					<-ctx.Done()
					return nil, failing(routing.ScopeEndpoint, false, suppliers.Failure{Cause: suppliers.Transport{}})
				}
			}, 0, uncounted,
		},
		{"200 and nothing in it", options{entries: 2}, func(h *harness) scripted { return serve(h.feed(evStart, evMsgDelta, evMsgStop)) }, 0, failed},
		{"a hangup before the commit point", options{entries: 2}, func(h *harness) scripted { return serve(h.feed(evStart), beat{hangup: true}) }, 0, failed},
		{"a hangup after it", options{entries: 2}, func(h *harness) scripted { return serve(h.feed(evStart, evBlock, evHello), beat{hangup: true}) }, 0, failed},
		{"idle before the commit point", options{entries: 2, startBy: 10 * time.Minute}, func(h *harness) scripted { return hold(h.feed(evStart)) }, 0, failed},
		{"the client leaves before the commit point", options{}, func(h *harness) scripted {
			return hold(h.feed(evStart), h.after(time.Hour, evBlock))
		}, time.Second, uncounted},
		{"the client leaves after it", options{}, func(h *harness) scripted {
			return hold(h.feed(evStart, evBlock, evHello), h.after(time.Hour, evBang))
		}, time.Second, succeeded},
		{"a compact reply", options{mutate: func(c *Call) { compact(t, c) }}, func(*harness) scripted { return wholeAnswer(compacted) }, 0, succeeded},
		{"an empty compact reply", options{mutate: func(c *Call) { compact(t, c) }}, func(*harness) scripted { return wholeAnswer("") }, 0, failed},
		// An endpoint that does not stop when the client does is read on for its usage: content that comes after the
		// client left was never delivered, so the stream breaking then is a failure while the client was gone.
		{"the client leaves, then content comes and the stream breaks", options{}, func(h *harness) scripted {
			return serve(h.feed(evStart), h.after(10*time.Second, evBlock, evHello), beat{hangup: true})
		}, time.Second, uncounted},
	}
	for _, tc := range cases {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, tc.o)
			h.health.Observe(ctx, quality.Observation{Endpoint: uuidOf(ep(1)), Failure: fivexx, Ended: time.Now()})
			h.up.eps[uuidOf(ep(1))].script = []scripted{tc.first(h)}
			if tc.o.entries == 2 {
				h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
			}
			h.runLeaving(tc.leave)
			want := map[int]float64{1: tc.want}
			if tc.o.entries == 2 {
				want[2] = 1
			}
			for n, health := range want {
				if got := h.health.Measured(uuidOf(ep(n)), time.Now()).Health; math.Abs(got-health) > 1e-9 {
					t.Errorf("%s: endpoint %d's health %v, want %v", tc.name, n, got, health)
				}
			}
		})
	}
}

// runLeaving runs the call, and has the client leave after leave unless it is zero.
func (h *harness) runLeaving(leave time.Duration) {
	if leave == 0 {
		h.run()
		return
	}
	done := h.start()
	time.Sleep(leave)
	h.client()
	<-done
	h.check()
}

// TestTheTimeToTheFirstTokenIsMeasured: the time from sending to the first content is what quality averages, once five
// attempts have measured it.
func TestTheTimeToTheFirstTokenIsMeasured(t *testing.T) {
	health := quality.New(nil)
	var now time.Time // the bubble's: every bubble's clock starts at the same instant
	for range 5 {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, options{health: health})
			h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(evStart), h.after(2*time.Second, evBlock, evHello, evBang, evBlockStop, evMsgDelta, evMsgStop))}
			h.run()
			now = time.Now()
		})
	}
	if got := health.Measured(uuidOf(ep(1)), now); got.Latency != 2*time.Second || got.Throughput != 0 {
		t.Errorf("measured %+v, want 2s to the first token and no speed (the answer is under 50 tokens)", got)
	}
}

// TestThroughputLeavesOutOurOwnWaits: the upstream's end is dated when it is read, so an extension still in flight then,
// which the attempt waits for before it closes, does not slow the measured speed: 100 tokens a second after the first
// content, though the attempt closes half a second after the upstream ended.
func TestThroughputLeavesOutOurOwnWaits(t *testing.T) {
	health := quality.New(nil)
	var now time.Time
	for range 5 {
		run(t, func(t *testing.T, ctx context.Context) {
			var asked atomic.Int32
			h := newHarness(t, ctx, segmented(options{health: health, extend: func(context.Context, billing.ExtendRequest) error {
				if asked.Add(1) > 1 { // the first is asked for before the first content, and answered at once
					time.Sleep(500 * time.Millisecond)
				}
				return nil
			}}))
			delta := strings.Replace(h.events[evMsgDelta], `"output_tokens": 15`, `"output_tokens": 100`, 1)
			h.up.eps[uuidOf(ep(1))].script = []scripted{serve(h.feed(evStart, evBlock, evHello),
				beat{wait: time.Second, data: text(130) + events(h.events, evBlockStop) + delta + events(h.events, evMsgStop)})}
			h.run()
			if h.elapsed() != 1500*time.Millisecond {
				t.Fatalf("the call took %v: no extension was waited for after the upstream's end", h.elapsed())
			}
			now = time.Now()
		})
	}
	if got := health.Measured(uuidOf(ep(1)), now).Throughput; math.Abs(got-100) > 1e-9 {
		t.Errorf("throughput %v, want 100 tokens a second", got)
	}
}
