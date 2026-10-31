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
	"axiom/internal/platform/textshape"
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

// TestASettlementIsRetriedUntilItIsWritten: the same outcome each time, so a try that committed with its
// acknowledgement lost costs nothing more; the ending carries the cost of the one written, and the call gives its slots
// back once it is written.
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
		if tries != 3 || !h.isReleased() {
			t.Errorf("%d tries, released %v", tries, h.isReleased())
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
		if h.isReleased() {
			t.Fatal("the call gave its slots back before its settlement was written")
		}
		<-h.released
		if h.elapsed() < time.Minute {
			t.Errorf("released after %v, before the settlement could be written", h.elapsed())
		}
		h.check()
	})
}

// TestASettlementThatCannotBeWrittenIsTheCleanups: one billing refuses for good is not retried and its slots go at once,
// since nothing else will write it; one retried in vain is given up when SettleBackground is over, and its slots go then.
// Either way nothing more is written, and the cleanup settles the reservation by recording the request lost.
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
			h := newHarness(t, ctx, options{settleErr: tc.err, config: func(c *Config) { c.SettleBackground = time.Minute }})
			h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
			<-h.start()
			time.Sleep(2 * time.Minute)
			h.mu.Lock()
			n, last := len(h.settlements), h.settlements[len(h.settlements)-1].at.Sub(h.started)
			h.mu.Unlock()
			if !tc.tries(n) || last != tc.took {
				t.Errorf("%s: %d tries, the last after %v", tc.name, n, last)
			}
			if !h.isReleased() {
				t.Errorf("%s: the slots are still held, and nothing will write the reservation", tc.name)
			}
			h.check()
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
		h.exec.Outstanding.Drain(stop, time.Second)
		<-done
		if h.elapsed() != 5*time.Second || !strings.Contains(h.w.body(), `"error_type":"timeout"`) {
			t.Errorf("drained after %v:\n%s", h.elapsed(), h.w.body())
		}
		if a := h.ended().outcome.Attempts[0]; a.Result != billing.Committed || a.Finish != billing.FinishTimeout {
			t.Errorf("attempt %+v", a)
		}
		h.check()
	})
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{settleErr: io.ErrUnexpectedEOF})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		<-h.start()
		stop, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		h.exec.Outstanding.Drain(stop, 30*time.Second)
		if h.elapsed() > h.cfg.SettleRetry+2*time.Second {
			t.Errorf("drained after %v; the 30s grace must not be added after the deadline", h.elapsed())
		}
		h.check()
	})
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		h.exec.Outstanding.Drain(ctx, time.Second) // nothing in flight: at once
		h.drained = true
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run() // a call after the drain starts no attempt
		if h.elapsed() != 0 || h.up.eps[uuidOf(ep(1))].sends != 0 || !strings.Contains(h.w.body(), `"timeout"`) {
			t.Errorf("after %v, %d attempts:\n%s", h.elapsed(), h.up.eps[uuidOf(ep(1))].sends, h.w.body())
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

// checkNotExecuted: an attempt known not to have run used nothing and cost nothing, and its supplier row says so as a
// complete estimate of nothing.
func checkNotExecuted(t *testing.T, a billing.AttemptResult) {
	t.Helper()
	if a.Result != billing.NotExecuted || a.Segments != nil || a.Cost != 0 {
		t.Errorf("not executed: %+v", a)
	}
	if a.Estimate.Flag != suppliers.CostComplete || a.Estimate.Native != (platform.Exact{}) {
		t.Errorf("not executed: the supplier's row is %+v, want complete and nothing: it is known to have cost nothing", a.Estimate)
	}
}

func checkCharged(t *testing.T, h *harness) {
	t.Helper()
	a := h.ended().outcome.Attempts
	if len(a) != 3 {
		t.Fatalf("attempts %+v", a)
	}
	input := estimatedInput(t, h, 1)
	checkNotExecuted(t, a[0])
	if a[1].Result != billing.Unknown || !usageIs(a[1].Segments, input, 0, 0) || a[1].Cost != 70 {
		t.Errorf("unknown: %+v", a[1])
	}
	if a[2].Result != billing.Committed || !usageIs(a[2].Segments, 25, 2, 1<<catalog.ItemInput) || a[2].Cost != 70 {
		t.Errorf("broken after the commit point: %+v", a[2])
	}
}

// TestACostThatIsNotKnown: a cost that leaves something out, or a pricing that failed, is booked as the cost that came
// back, and an error is logged. Nothing corrects it in M1: the cost is the platform's, and the charge to the user never
// rests on it. A failure is never a cost the platform knows: the supplier's row of it is flagged unpriced, not complete.
func TestACostThatIsNotKnown(t *testing.T) {
	errPricing := errors.New("the cost schedule could not be read")
	for _, tc := range []struct {
		name     string
		o        options
		cost     int64
		unpriced bool // the supplier's row must say so
	}{
		{"a cost that leaves something out", options{cost: 70, costNotKnown: true}, 70, false},
		{"a pricing that failed", options{cost: 70, costErr: errPricing}, 70, true},
	} {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, tc.o)
			h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
			h.run()
			a := h.ended().outcome.Attempts
			if len(a) != 1 {
				t.Fatalf("%s: attempts %+v", tc.name, a)
			}
			if a[0].Cost != tc.cost {
				t.Errorf("%s: cost %d, want %d", tc.name, a[0].Cost, tc.cost)
			}
			if tc.unpriced && a[0].Estimate.Flag != suppliers.CostUnpriced {
				t.Errorf("%s: the supplier's row is flagged %q, want unpriced: a failure is not a cost known to be zero", tc.name, a[0].Estimate.Flag)
			}
		})
	}
}

// estimatedInput is the input of the call's entry with its text at 4 bytes a token, which must be below the admission's
// bound for the test to tell them apart.
func estimatedInput(t *testing.T, h *harness, entry int) int64 {
	t.Helper()
	d := routing.DemandOf(&h.call.Request)
	bound, _ := billing.InputBound(d, h.call.Plan.Entries[entry].Limits)
	d.TextBytes = (d.TextBytes + 3) / 4
	input, _ := billing.InputBound(d, h.call.Plan.Entries[entry].Limits)
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
	physical := func(t *testing.T, u catalog.Usage, n int64) catalog.Usage {
		t.Helper()
		v, err := u.WithPhysicalInput(n)
		if err != nil {
			t.Fatal(err)
		}
		return v
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
		{
			// A report accepted, then made invalid by a count that went down: the counts it took stay, and the estimate
			// of the whole prompt stands in their place, not on top of them. 100 prompt tokens are 20 plain and 80 cached;
			// the estimate of 900 adds the 800 they are short of, not another 900 (#339).
			"an accepted report that turned invalid",
			protocol.Usage{
				Segments: []protocol.Segment{{Usage: physical(t, usage(map[catalog.Item]int64{in: 20, cached: 80}), 100)}},
				Invalid:  errors.New("the input went down"),
			},
			protocol.Progress{ContentBytes: 7},
			map[catalog.Item]int64{in: 820, cached: 80, out: 3},
		},
		{
			// The kept cache items add up past the physical total they overlap: 80 cached and 80 written within a
			// 100-token prompt. The estimate of 900 is short of that sum by 740, and those 740 are the whole of what the
			// plain input becomes; adding 800 (900 less the physical total) would charge the prompt twice (#339).
			"an accepted report whose cache items overlap the prompt total",
			protocol.Usage{
				Segments: []protocol.Segment{{Usage: physical(t, usage(map[catalog.Item]int64{cached: 80, catalog.ItemCacheWrite: 80}), 100)}},
				Invalid:  errors.New("the input went down"),
			},
			protocol.Progress{ContentBytes: 7},
			map[catalog.Item]int64{in: 740, cached: 80, catalog.ItemCacheWrite: 80, out: 3},
		},
	} {
		// 3600 bytes of text at 4 a token, inside the model's window: the first sampling's input is 900.
		got := estimated(tc.usage, tc.progress, billing.Demand{TextBytes: 3600}, billing.Model{ContextLength: 1 << 20}, math.MaxInt64, billing.TokenizerNone)
		for it := range catalog.NumItems {
			if n := got[0].Usage.Count(it); n != tc.want[it] {
				t.Errorf("%s: %s is %d, want %d", tc.name, it, n, tc.want[it])
			}
		}
	}
}

// TestAnEstimateOfImagesAndFiles: an input the upstream did not report is charged for an image at 1600 tokens and a file
// at 4096 beside the text at 4 bytes a token, not at the whole context the reservation had to allow for them; a bound the
// model's data gives that is below those holds, so does the window, and a report of the input, in whole or by its cache
// part, is not estimated over.
func TestAnEstimateOfImagesAndFiles(t *testing.T) {
	t.Parallel()
	const in, cached = catalog.ItemInput, catalog.ItemCacheRead
	reported := func(it catalog.Item, n int64) protocol.Usage {
		u, err := catalog.NewUsage(map[catalog.Item]int64{it: n}, catalog.ServiceTierStandard, nil)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Usage{Segments: []protocol.Segment{{Usage: u, Reported: 1 << it}}}
	}
	demand := billing.Demand{TextBytes: 4001, Images: 1, Documents: 1} // 1001 tokens of text
	for _, tc := range []struct {
		name  string
		usage protocol.Usage
		model billing.Model
		want  int64
	}{
		{"a model whose images and files have no known size", protocol.Usage{}, billing.Model{ContextLength: 200000}, 1001 + 1600 + 4096},
		{"an image bound below the estimate", protocol.Usage{}, billing.Model{ContextLength: 200000, ImageTokens: 500}, 1001 + 500 + 4096},
		{"a file bound below the estimate", protocol.Usage{}, billing.Model{ContextLength: 200000, PageTokens: 100, FilePages: 2}, 1001 + 1600 + 200},
		{"a small window", protocol.Usage{}, billing.Model{ContextLength: 4000}, 4000},
		{"the input reported", reported(in, 12), billing.Model{ContextLength: 200000}, 12},
		{"the cached part of the input reported", reported(cached, 12), billing.Model{ContextLength: 200000}, 0},
	} {
		got := estimated(tc.usage, protocol.Progress{}, demand, tc.model, math.MaxInt64, billing.TokenizerNone)
		if n := got[0].Usage.Count(in); n != tc.want {
			t.Errorf("%s: the input is %d, want %d", tc.name, n, tc.want)
		}
	}
}

// TestAnEstimateOfCountedText: the input an upstream did not report is the request's own text counted in the tokens of
// the endpoint's model family where that family publishes a tokenizer, not its bytes at 4 a token; a family without one
// keeps the rule, the count is held to the window, and a reported input is never estimated over.
func TestAnEstimateOfCountedText(t *testing.T) {
	t.Parallel()
	const in = catalog.ItemInput
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"` +
		strings.Repeat("今天天气很好，我们一起去公园散步。", 500) + `"}]}`)
	req, err := protocol.Inspect(protocol.Messages, body)
	if err != nil {
		t.Fatal(err)
	}
	rule := (req.TextBytes + billing.InputBytesPerToken - 1) / billing.InputBytesPerToken
	d := billing.Demand{TextBytes: req.TextBytes}
	cl100k := billing.Model{ContextLength: 100000}
	counted, err := textTokens(body, d, billing.TokenizerCl100k)
	if err != nil {
		t.Fatal(err)
	}
	if counted <= rule {
		t.Errorf("counted %d, no more than the bytes rule's %d, on text the rule undercounts", counted, rule)
	}
	for _, tc := range []struct {
		name  string
		model billing.Model
		tok   billing.Tokenizer
		want  int64
	}{
		{"cl100k's own count", cl100k, billing.TokenizerCl100k, counted},
		{"a window below the count", billing.Model{ContextLength: 3000}, billing.TokenizerCl100k, 3000},
		{"a family with no tokenizer", billing.Model{ContextLength: 100000}, billing.TokenizerNone, rule},
	} {
		d := d
		d.TextTokens, err = textTokens(body, d, tc.tok)
		if err != nil {
			t.Fatal(err)
		}
		if tc.tok == billing.TokenizerNone && d.TextTokens != 0 {
			t.Errorf("%s: counted %d with no tokenizer", tc.name, d.TextTokens)
		}
		got := estimated(protocol.Usage{}, protocol.Progress{}, d, tc.model, math.MaxInt64, billing.TokenizerNone)
		if n := got[0].Usage.Count(in); n != tc.want {
			t.Errorf("%s: the input is %d, want %d", tc.name, n, tc.want)
		}
	}
	// A reported input is kept, whatever the count says it could have been.
	d.TextTokens = counted
	u, err := catalog.NewUsage(map[catalog.Item]int64{in: 12}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	reported := protocol.Usage{Segments: []protocol.Segment{{Usage: u, Reported: 1 << in}}}
	if n := estimated(reported, protocol.Progress{}, d, cl100k, math.MaxInt64, billing.TokenizerNone)[0].Usage.Count(in); n != 12 {
		t.Errorf("the reported input of 12 was estimated over: %d", n)
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
		got := estimated(tc.usage, protocol.Progress{ContentBytes: tc.bytes}, billing.Demand{}, billing.Model{}, 1000, billing.TokenizerNone)
		for _, it := range []catalog.Item{out, reasoning} {
			if n := got[0].Usage.Count(it); n != tc.want[it] {
				t.Errorf("%s: %s is %d, want %d", tc.name, it, n, tc.want[it])
			}
		}
	}
}

// TestOutputEstimateByShape: an output the upstream did not report is the tokens its text's shape takes in the entry
// family's weights, over every content byte, raw tool input beyond the tallied text included; a family without weights,
// or a reply whose content held no text to tally, keeps 3 bytes a token; either is held to the output limit and never
// below a count reported so far.
func TestOutputEstimateByShape(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 20) // 900 bytes, 180 words, 180 spaces, 20 dots
	var shape textshape.Shape
	shape.Add([]byte(text))
	lower, err := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemOutput: 500}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		tok   billing.Tokenizer
		p     protocol.Progress
		usage protocol.Usage
		limit int64
		want  int64
	}{
		// 180×1.52 + 20×0.89 + 180×0.21 = 329.2
		{"Claude Opus 4.7's weights", billing.TokenizerClaude47, protocol.Progress{ContentBytes: 900, Text: shape}, protocol.Usage{}, 1000, 330},
		// 180×1.24 + 20×0.66 + 180×0.14 = 261.6
		{"the earlier Claude's", billing.TokenizerClaude, protocol.Progress{ContentBytes: 900, Text: shape}, protocol.Usage{}, 1000, 262},
		{"raw input beside the text", billing.TokenizerClaude47, protocol.Progress{ContentBytes: 1800, Text: shape}, protocol.Usage{}, 1000, 660},
		{"held to the limit", billing.TokenizerClaude47, protocol.Progress{ContentBytes: 900, Text: shape}, protocol.Usage{}, 100, 100},
		{"two tool calls on Claude 4.7", billing.TokenizerClaude47, protocol.Progress{ContentBytes: 900, Text: shape, ToolCalls: 2}, protocol.Usage{}, 1000, 420},
		{"a tool call on the earlier Claude", billing.TokenizerClaude, protocol.Progress{ContentBytes: 900, Text: shape, ToolCalls: 1}, protocol.Usage{}, 1000, 307},
		{"a tool call held to the limit", billing.TokenizerClaude47, protocol.Progress{ContentBytes: 900, Text: shape, ToolCalls: 2}, protocol.Usage{}, 100, 100},
		{"a tool call on o200k adds nothing", billing.TokenizerO200k, protocol.Progress{ContentBytes: 900, ToolCalls: 2}, protocol.Usage{}, 1000, 300},
		{"a family without weights", billing.TokenizerNone, protocol.Progress{ContentBytes: 900, Text: shape}, protocol.Usage{}, 1000, 300},
		{"no text tallied", billing.TokenizerClaude47, protocol.Progress{ContentBytes: 900}, protocol.Usage{}, 1000, 300},
		{
			"a reported lower bound above it", billing.TokenizerClaude47,
			protocol.Progress{ContentBytes: 900, Text: shape},
			protocol.Usage{Segments: []protocol.Segment{{Usage: lower}}},
			1000, 500,
		},
	} {
		got := estimated(tc.usage, tc.p, billing.Demand{}, billing.Model{}, tc.limit, tc.tok)
		if n := got[0].Usage.Count(catalog.ItemOutput); n != tc.want {
			t.Errorf("%s: output %d, want %d", tc.name, n, tc.want)
		}
	}
}

// TestDrainDoesNotAddGracePastTheDeadline: the drain's grace is part of the node's deadline, not a new wait appended
// after it. A settlement still blocked when the deadline arrives must make Drain return there, not five seconds later;
// the controlled clock proves the whole path, not just the arithmetic.
func TestDrainDoesNotAddGracePastTheDeadline(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		release := make(chan struct{})
		h := newHarness(t, ctx, options{settle: func(int) error {
			<-release
			return nil
		}})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		done := h.start()
		time.Sleep(time.Second) // the attempt is over; its settlement is blocked
		stop, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		h.exec.Outstanding.Drain(stop, 5*time.Second)
		if h.elapsed() != 11*time.Second {
			t.Errorf("drain returned after %v; want the deadline at 11s, not a grace after it", h.elapsed())
		}
		close(release)
		<-done
		h.check()
	})
}
