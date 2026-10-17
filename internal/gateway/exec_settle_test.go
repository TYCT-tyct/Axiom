package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// isReleased reports whether the call has given its slots back.
func (h *harness) isReleased() bool {
	select {
	case <-h.released:
		return true
	default:
		return false
	}
}

func (h *harness) settledCalls() int {
	h.up.mu.Lock()
	defer h.up.mu.Unlock()
	return h.up.settled
}

// TestASettlementIsRetriedUntilItIsWritten: the same outcome each time, so a try that committed with its
// acknowledgement lost costs nothing more; the ending carries the cost of the one written, and the journal is told.
func TestASettlementIsRetriedUntilItIsWritten(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{
			final: billing.Final{Charged: platform.AmountExact(5000)},
			settle: func(try int) error {
				if try < 3 {
					return io.ErrUnexpectedEOF
				}
				return nil
			},
		})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run()
		body := h.w.body()
		if !strings.Contains(body, `"cost":0.000005,"cost_currency":"USD"`) || strings.Contains(body, "billing_pending") {
			t.Errorf("the ending:\n%s", body)
		}
		h.mu.Lock()
		tries := len(h.settlements)
		h.mu.Unlock()
		if tries != 3 || h.settledCalls() != 1 {
			t.Errorf("%d tries, the journal told %d times", tries, h.settledCalls())
		}
	})
}

// TestASettlementNotWrittenInTimeGoesOnInTheBackground: after 10s the ending goes out without a cost and says the
// billing is pending; the settlement goes on with the same outcome, and the call keeps its slots until it is written.
func TestASettlementNotWrittenInTimeGoesOnInTheBackground(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		start := time.Now()
		h := newHarness(t, ctx, options{settle: func(int) error {
			if time.Since(start) < time.Minute {
				return io.ErrUnexpectedEOF
			}
			return nil
		}})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		<-h.start()
		if h.elapsed() != h.cfg.SettleRetry || !strings.Contains(h.w.body(), `"billing_pending":true`) || strings.Contains(h.w.body(), `"cost"`) {
			t.Fatalf("after %v:\n%s", h.elapsed(), h.w.body())
		}
		if h.isReleased() || h.settledCalls() != 0 {
			t.Fatal("the call gave its slots back before its settlement was written")
		}
		<-h.released
		if h.elapsed() < time.Minute || h.settledCalls() != 1 {
			t.Errorf("released after %v, the journal told %d times", h.elapsed(), h.settledCalls())
		}
		h.check()
	})
}

// TestASettlementThatCannotBeWrittenIsTheCleanups: one billing refuses for good is not retried, and one retried in
// vain is given up when SettleBackground is over; either way the slots go only once the journal has written what
// the cleanup settles the call by.
func TestASettlementThatCannotBeWrittenIsTheCleanups(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		tries func(n int) bool
		took  time.Duration
	}{
		{"no attempt to price", fmt.Errorf("settle: %w", billing.ErrNoAttempt), func(n int) bool { return n == 1 }, 0},
		{"no reservation", fmt.Errorf("read it: %w", pg.ErrNoRows), func(n int) bool { return n == 1 }, 0},
		{"the database down", io.ErrUnexpectedEOF, func(n int) bool { return n > 10 }, 10*time.Second + time.Minute},
	} {
		run(t, func(t *testing.T, ctx context.Context) {
			flushed := make(chan struct{})
			h := newHarness(t, ctx, options{settleErr: tc.err, flushed: flushed, config: func(c *Config) { c.SettleBackground = time.Minute }})
			h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
			<-h.start()
			time.Sleep(2 * time.Minute)
			if h.isReleased() {
				t.Fatalf("%s: the slots went before the journal was written", tc.name)
			}
			h.mu.Lock()
			n, last := len(h.settlements), h.settlements[len(h.settlements)-1].at.Sub(h.started)
			h.mu.Unlock()
			if !tc.tries(n) || last != tc.took {
				t.Errorf("%s: %d tries, the last after %v", tc.name, n, last)
			}
			close(flushed)
			h.check()
			if h.settledCalls() != 0 {
				t.Errorf("%s: the journal was told a settlement was written", tc.name)
			}
		})
	}
}

// TestDrainCancelsTheUpstreamsAndLeavesTheRestToTheCleanup: a node going away waits for its calls until its time is
// up, then cancels every upstream, so the calls end with a timeout and settle in the grace; settlements still retrying
// when that is over are given up, the cleanup's to finish.
func TestDrainCancelsTheUpstreamsAndLeavesTheRestToTheCleanup(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{policy: suppliers.CallPolicy{Streams: true}}) // not even one that ignores the client is spared
		h.up.eps[uuidOf(ep(1))].script = []scripted{hold(h.feed(evStart, evBlock, evHello))}
		done := h.start()
		time.Sleep(time.Second) // the call is under way
		stop, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		h.exec.Drain(stop, 30*time.Second)
		<-done
		if h.elapsed() != 6*time.Second || !strings.Contains(h.w.body(), `"error_type":"timeout"`) {
			t.Errorf("drained after %v:\n%s", h.elapsed(), h.w.body())
		}
		if a := h.ended().outcome.Attempts[0]; a.Result != billing.Committed || a.Finish != billing.FinishTimeout {
			t.Errorf("attempt %+v", a)
		}
		h.check()
	})
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{settleErr: io.ErrUnexpectedEOF, flushed: make(chan struct{})})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		<-h.start()
		stop, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		h.exec.Drain(stop, 30*time.Second)
		if !h.isReleased() || h.elapsed() != h.cfg.SettleRetry+31*time.Second {
			t.Errorf("drained after %v, released %v", h.elapsed(), h.isReleased())
		}
		h.check()
	})
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		h.exec.Drain(ctx, time.Second) // nothing in flight: at once, and nothing is cancelled
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run()
		if h.elapsed() != 0 || h.ended().outcome.Attempts[0].Finish != billing.FinishStop {
			t.Errorf("after %v, outcome %+v", h.elapsed(), h.ended().outcome)
		}
	})
}

// TestWhatIsCharged: the outcome of each attempt, as billing charges it. One known not to have run used and cost
// nothing; one whose result is unknown is the input estimated, its text at 4 bytes a token, at the cost of that usage;
// an answer that broke after the content began has the output read at 3 bytes a token, the input it reported, and its
// cost.
func TestWhatIsCharged(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 3, cost: 70})
		h.up.eps[uuidOf(ep(1))].script = []scripted{failure(routing.ScopeEndpoint, true)}
		h.up.eps[uuidOf(ep(2))].script = []scripted{failure(routing.ScopeEndpoint, false)}
		h.up.eps[uuidOf(ep(3))].script = []scripted{serve(h.feed(evStart, evBlock, evHello), beat{hangup: true})}
		h.run()
		checkCharged(t, h)
	})
}

func checkCharged(t *testing.T, h *harness) {
	t.Helper()
	a := h.ended().outcome.Attempts
	if len(a) != 3 {
		t.Fatalf("attempts %+v", a)
	}
	input := estimatedInput(t, h)
	checkCosted(t, a)
	if a[0].Result != billing.NotExecuted || a[0].Segments != nil || a[0].Cost != 0 {
		t.Errorf("not executed: %+v", a[0])
	}
	if a[1].Result != billing.Unknown || !usageIs(a[1].Segments, input, 0, 0) || a[1].Cost != 70 {
		t.Errorf("unknown: %+v", a[1])
	}
	if a[2].Result != billing.Committed || !usageIs(a[2].Segments, 25, 2, 1<<catalog.ItemInput) || a[2].Cost != 70 {
		t.Errorf("broken after the commit point: %+v", a[2])
	}
}

// checkCosted: every attempt's cost is known, the one known not to have run as well as those priced.
func checkCosted(t *testing.T, a []billing.AttemptResult) {
	t.Helper()
	for _, at := range a {
		if !at.Costed {
			t.Errorf("attempt %d: its cost is not known", at.Seq)
		}
	}
}

// TestACostThatIsNotKnown: a cost that leaves something out, or a pricing that failed, reaches billing as not known,
// with whatever cost came back: the waiver cap then counts the attempt at its sale price
// (docs/domains/billing.md「免单规则」). A failure is never a known cost, whatever the implementation says.
func TestACostThatIsNotKnown(t *testing.T) {
	errPricing := errors.New("the cost schedule could not be read")
	for _, tc := range []struct {
		name string
		o    options
		cost int64
	}{
		{"a cost that leaves something out", options{cost: 70, costNotKnown: true}, 70},
		{"a pricing that failed", options{cost: 70, costErr: errPricing}, 70},
	} {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, tc.o)
			h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
			h.run()
			a := h.ended().outcome.Attempts
			if len(a) != 1 {
				t.Fatalf("%s: attempts %+v", tc.name, a)
			}
			if a[0].Costed || a[0].Cost != tc.cost {
				t.Errorf("%s: cost %d, known %v; want %d, not known", tc.name, a[0].Cost, a[0].Costed, tc.cost)
			}
		})
	}
}

// estimatedInput is the input of the call's second entry with its text at 4 bytes a token, which must be below the
// admission's bound for the test to tell them apart.
func estimatedInput(t *testing.T, h *harness) int64 {
	t.Helper()
	d := routing.DemandOf(&h.call.Request)
	bound, _ := billing.InputBound(d, h.call.Plan.Entries[1].Limits)
	d.TextBytes = (d.TextBytes + 3) / 4
	input, _ := billing.InputBound(d, h.call.Plan.Entries[1].Limits)
	if input >= bound {
		t.Fatalf("the estimate %d is not below the bound %d: the test proves nothing", input, bound)
	}
	return input
}

// usageIs: one sampling, of that input and output, and those items reported.
func usageIs(segments []billing.Segment, input, output int64, reported catalog.ItemSet) bool {
	return len(segments) == 1 && segments[0].Usage.Count(catalog.ItemInput) == input &&
		segments[0].Usage.OutputTotal() == output && segments[0].Reported == reported
}

// TestEstimated: the upstream's report stands for what it reported; what it did not is estimated, the input at 4 bytes
// of the request's text a token and the output at 3 bytes read a token, both rounded up, the output never below a count
// reported.
func TestEstimated(t *testing.T) {
	t.Parallel()
	usage := func(counts map[catalog.Item]int64) catalog.Usage {
		u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	const in, out, cached, search = catalog.ItemInput, catalog.ItemOutput, catalog.ItemCacheRead, catalog.ItemWebSearch
	all := catalog.ItemSet(1<<in | 1<<out | 1<<search)
	for _, tc := range []struct {
		name     string
		usage    protocol.Usage
		progress protocol.Progress
		want     map[catalog.Item]int64
	}{
		{
			"all reported",
			protocol.Usage{Segments: []protocol.Segment{{Usage: usage(map[catalog.Item]int64{in: 10, out: 3}), Reported: all}}},
			protocol.Progress{ContentBytes: 50, Searches: 2},
			map[catalog.Item]int64{in: 10, out: 3},
		},
		{"nothing", protocol.Usage{}, protocol.Progress{ContentBytes: 50, Searches: 2}, map[catalog.Item]int64{in: 900, out: 17, search: 2}},
		{"no bytes", protocol.Usage{}, protocol.Progress{}, map[catalog.Item]int64{in: 900}},
		{"bytes that divide", protocol.Usage{}, protocol.Progress{ContentBytes: 51}, map[catalog.Item]int64{in: 900, out: 17}},
		{
			"the input from the cache",
			protocol.Usage{Segments: []protocol.Segment{{Usage: usage(map[catalog.Item]int64{cached: 400}), Reported: 1 << cached}}},
			protocol.Progress{},
			map[catalog.Item]int64{cached: 400},
		},
		{
			"an output count above the bytes",
			protocol.Usage{Segments: []protocol.Segment{{Usage: usage(map[catalog.Item]int64{in: 10, out: 80}), Reported: 1 << in}}},
			protocol.Progress{ContentBytes: 50},
			map[catalog.Item]int64{in: 10, out: 80},
		},
		{
			"reasoning stays",
			protocol.Usage{Segments: []protocol.Segment{{Usage: usage(map[catalog.Item]int64{in: 10, catalog.ItemReasoning: 30}), Reported: 1 << in}}},
			protocol.Progress{ContentBytes: 150},
			map[catalog.Item]int64{in: 10, catalog.ItemReasoning: 30, out: 20},
		},
		{"bytes past any count", protocol.Usage{}, protocol.Progress{ContentBytes: 1 << 40}, map[catalog.Item]int64{in: 900, out: maxCount}},
		{"a count reported above the ratio", protocol.Usage{Segments: []protocol.Segment{{Usage: usage(map[catalog.Item]int64{out: 40})}}}, protocol.Progress{ContentBytes: 30}, map[catalog.Item]int64{in: 900, out: 40}},
		{
			"a report found invalid",
			protocol.Usage{Segments: []protocol.Segment{{Usage: usage(map[catalog.Item]int64{in: 10})}}, Invalid: errors.New("went down")},
			protocol.Progress{ContentBytes: 7},
			map[catalog.Item]int64{in: 900, out: 3},
		},
	} {
		// 3600 bytes of text at 4 a token, inside the model's window: the first sampling's input is 900.
		got := estimated(tc.usage, tc.progress, billing.Demand{TextBytes: 3600}, billing.Model{ContextLength: 1 << 20}, math.MaxInt64)
		for it := range catalog.NumItems {
			if n := got[0].Usage.Count(it); n != tc.want[it] {
				t.Errorf("%s: %s is %d, want %d", tc.name, it, n, tc.want[it])
			}
		}
	}
}

// TestAnEstimateStaysWithinTheOutputLimit: the bytes of a stream are read at 3 bytes a token, rounded up, and what they
// say is held to the output limit the attempt was quoted at, the one metering holds it to, so a stream that never
// reported its usage is not charged for more than the reservation covers; a count the upstream did report stays
// whatever it is, and reasoning is part of the limit.
func TestAnEstimateStaysWithinTheOutputLimit(t *testing.T) {
	t.Parallel()
	usage := func(counts map[catalog.Item]int64) catalog.Usage {
		u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	const out, reasoning = catalog.ItemOutput, catalog.ItemReasoning
	reported := func(counts map[catalog.Item]int64) protocol.Usage {
		return protocol.Usage{Segments: []protocol.Segment{{Usage: usage(counts), Reported: 1 << reasoning}}}
	}
	for _, tc := range []struct {
		name  string
		usage protocol.Usage
		bytes int64
		want  map[catalog.Item]int64
	}{
		{"bytes past the limit", protocol.Usage{}, 4000, map[catalog.Item]int64{out: 1000}},
		{"bytes within it", protocol.Usage{}, 2400, map[catalog.Item]int64{out: 800}},
		{"a ratio that rounds up to the limit", protocol.Usage{}, 2999, map[catalog.Item]int64{out: 1000}},
		{"reasoning is part of the limit", reported(map[catalog.Item]int64{reasoning: 300}), 4000, map[catalog.Item]int64{reasoning: 300, out: 700}},
		{"a count above the limit stays", reported(map[catalog.Item]int64{reasoning: 1200}), 4000, map[catalog.Item]int64{reasoning: 1200}},
	} {
		got := estimated(tc.usage, protocol.Progress{ContentBytes: tc.bytes}, billing.Demand{}, billing.Model{}, 1000)
		for _, it := range []catalog.Item{out, reasoning} {
			if n := got[0].Usage.Count(it); n != tc.want[it] {
				t.Errorf("%s: %s is %d, want %d", tc.name, it, n, tc.want[it])
			}
		}
	}
}
