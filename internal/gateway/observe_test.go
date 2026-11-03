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
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
	"axiom/internal/websearch"
)

// TestEveryAttemptIsObservedOnce: each attempt tells quality how it ended, once, and the table decides what it counts
// as. Endpoint 1 starts from one counted failure (health 0.8), so an attempt the table counts as a success lifts it to
// 0.84, one it counts as a failure drops it to 0.64, and one it does not count leaves it at 0.8; endpoint 2, where the
// call goes next, ends normally.
func TestEveryAttemptIsObservedOnce(t *testing.T) {
	t.Parallel()
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
				if got := h.health.MeasuredFor(suppliers.Routable{ID: uuidOf(ep(n))}, time.Now()).Health; math.Abs(got-health) > 1e-9 {
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
	t.Parallel()
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
	if got := health.MeasuredFor(suppliers.Routable{ID: uuidOf(ep(1))}, now); got.Latency != 2*time.Second || got.Throughput != 0 {
		t.Errorf("measured %+v, want 2s to the first token and no speed (the answer is under 50 tokens)", got)
	}
}

// TestThroughputLeavesOutOurOwnWaits: the upstream's end is dated when it is read, so an extension still in flight then,
// which the attempt waits for before it closes, does not slow the measured speed: 100 tokens a second after the first
// content, though the attempt closes half a second after the upstream ended.
func TestThroughputLeavesOutOurOwnWaits(t *testing.T) {
	t.Parallel()
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
	if got := health.MeasuredFor(suppliers.Routable{ID: uuidOf(ep(1))}, now).Throughput; math.Abs(got-100) > 1e-9 {
		t.Errorf("throughput %v, want 100 tokens a second", got)
	}
}

// TestClassEvidenceIsOneReportedResponse: the usage a source class rule reads is what the upstream reported, not the
// gateway's estimate, and it comes from the same response as the headers, so two hops of a search loop cannot together
// satisfy a rule no single response did.
func TestClassEvidenceIsOneReportedResponse(t *testing.T) {
	const cached = `"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14,"prompt_tokens_details":{"cached_tokens":5}}`
	reported := []quality.Condition{{Evidence: "usage", Item: "input", Operator: "present"}}
	together := []quality.Condition{
		{Evidence: "header", Name: "X-Official", Operator: "present"},
		{Evidence: "usage", Item: "cache_read", Operator: "present"},
	}
	official := http.Header{"X-Official": {"1"}}
	for _, tc := range []struct {
		name  string
		rule  []quality.Condition
		hops  []scripted
		class string
	}{
		{"input reported", reported, []scripted{serve(beat{data: chatAnswer})}, "official"},
		{"input estimated", reported, []scripted{serve(beat{data: withoutUsage(chatAnswer)})}, "relay"},
		{"header and usage in the first hop", together, []scripted{
			withHeader(official, serve(beat{data: strings.Replace(chatToolCall, `"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14}`, cached, 1)})),
			serve(beat{data: chatAnswer}),
		}, "official"},
		{"header in the first hop, usage in the second", together, []scripted{
			withHeader(official, serve(beat{data: chatToolCall})),
			serve(beat{data: strings.Replace(chatAnswer, `"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}`, cached, 1)}),
		}, "relay"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run(t, func(t *testing.T, ctx context.Context) {
				if got := classAfter(t, ctx, tc.rule, tc.hops); got != tc.class {
					t.Errorf("class %q, want %q", got, tc.class)
				}
			})
		})
	}
}

// classAfter runs one search-loop call whose hops answer as given and returns the class quality then gives endpoint 1,
// where official is recognized by rule.
func classAfter(t *testing.T, ctx context.Context, rule []quality.Condition, hops []scripted) string {
	t.Helper()
	health := quality.New(nil)
	classes := quality.DefaultSourceClasses()
	classes[0].Rules = quality.Rules{Match: "all", Conditions: rule}
	if err := health.SetSourceClasses(classes); err != nil {
		t.Fatal(err)
	}
	// The harness's entry: no catalog model or formats, upstream up-1.
	endpoint := suppliers.Routable{ID: uuidOf(ep(1)), Upstream: "up-1"}
	if err := health.Prepare(ctx, []suppliers.Routable{endpoint}); err != nil {
		t.Fatal(err)
	}
	upstream := protocol.Chat
	h := newHarness(t, ctx, options{
		client: protocol.Chat, upstream: &upstream, request: []byte(searchTool), health: health,
		price:  `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_read":"0.0000001","cache_write":"0.000001","web_search":"0.01"}}`,
		search: &recordingSearcher{results: []websearch.Result{{URL: "https://example.com", Title: "E", Content: "R"}}},
		server: &protocol.ServerWebSearch{Engine: "exa", MaxResults: 2},
		script: map[int][]scripted{1: hops},
	})
	h.run()
	if h.w.status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", h.w.status, h.w.body())
	}
	return health.MeasuredFor(endpoint, time.Now()).Class
}

// withoutUsage drops a Chat stream's usage chunk: the upstream reported nothing.
func withoutUsage(stream string) string {
	var kept []string
	for _, e := range strings.SplitAfter(stream, "\n\n") {
		if !strings.Contains(e, `"usage"`) {
			kept = append(kept, e)
		}
	}
	return strings.Join(kept, "")
}

// withHeader answers as s does, with h as the response headers.
func withHeader(h http.Header, s scripted) scripted {
	return func(ctx context.Context) (*http.Response, *suppliers.Failure) {
		resp, f := s(ctx)
		if resp != nil {
			resp.Header = h
		}
		return resp, f
	}
}
