package billing

import (
	"bytes"
	"errors"
	"math"
	"os"
	"slices"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

// meter binds the first entry of a one-entry route with 8-token segments, extending at half a segment. On prices,
// 80 bytes of text and no tools: fixed 80, allowance 150 tokens at 10 = 1500, each output token 10.
func meter(tb testing.TB, d Demand, e Quotable) *Meter {
	tb.Helper()
	r, err := Quote(d, []Quotable{e}, Segments{Output: 8, Overshoot: segments.Overshoot}, nil)
	if err != nil {
		tb.Fatal(err)
	}
	r.trigger = 500
	return r.Bind(0)
}

func usage(tb testing.TB, c map[catalog.Item]int64) catalog.Usage {
	tb.Helper()
	u, err := catalog.NewUsage(c, catalog.ServiceTierStandard, nil)
	if err != nil {
		tb.Fatal(err)
	}
	return u
}

// target is the cumulative reservation covering tokens of output, on prices with 80 bytes and no tools.
func target(tb testing.TB, tokens int64) string {
	tb.Helper()
	return units(tb, amount(tb, 80+1500+10*tokens))
}

// TestMeterSample walks the recorded Messages stream: message_start reports output 1, a lower bound, so "Hello"
// meters 5 by its bytes and passes the extension point (4 of the first 8 tokens); the final message_delta reports 15,
// past the next point (12 of 16), and is metered as reported.
func TestMeterSample(t *testing.T) {
	t.Parallel()

	m := meter(t, Demand{TextBytes: 80}, entry(t, true, 3))
	f, err := os.ReadFile("../protocol/testdata/messages/text/upstream.sse")
	if err != nil {
		t.Fatal(err)
	}
	var p Progress
	var got []string
	for line := range bytes.Lines(f) {
		data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data: "))
		if !ok {
			continue
		}
		type counts struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		}
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Usage counts `json:"usage"`
			} `json:"message"`
			Delta struct {
				Text string `json:"text"`
			} `json:"delta"`
			Usage counts `json:"usage"`
		}
		if err := jsonx.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		switch event.Type {
		case "message_start":
			u := event.Message.Usage
			p.Reported, p.Final = usage(t, map[catalog.Item]int64{in: u.InputTokens, catalog.ItemOutput: u.OutputTokens}), 1<<in
		case "content_block_delta":
			p.ContentBytes += int64(len(event.Delta.Text))
		case "message_delta":
			p.Reported = usage(t, map[catalog.Item]int64{in: p.Reported.Count(in), catalog.ItemOutput: event.Usage.OutputTokens})
			p.Final.Add(catalog.ItemOutput)
		}
		step := m.Observe(p)
		if step.Exhausted {
			got = append(got, event.Type+" exhausted")
		}
		if step.Extend {
			got = append(got, event.Type+" extend "+units(t, step.Request.Target))
			m.Extended(step.Request.Seq, nil)
		}
	}
	want := []string{"content_block_delta extend " + target(t, 16), "message_delta extend " + target(t, 24)}
	if !slices.Equal(got, want) {
		t.Errorf("steps %q, want %q", got, want)
	}
}

// TestMeterOutput: output is the larger of the reported count and the byte bound, complete events and the partial
// one, until the count is final; it never passes the output limit (500). Shown by where the first segment of 8
// tokens runs out.
func TestMeterOutput(t *testing.T) {
	t.Parallel()

	out := func(n int64) catalog.Usage { return usage(t, map[catalog.Item]int64{catalog.ItemOutput: n}) }
	reasoned := usage(t, map[catalog.Item]int64{catalog.ItemOutput: 4, catalog.ItemReasoning: 5})
	for _, tc := range []struct {
		name      string
		p         Progress
		exhausted bool
	}{
		{"bytes within the segment", Progress{ContentBytes: 5, PartialBytes: 3}, false},
		{"bytes past it", Progress{ContentBytes: 5, PartialBytes: 4}, true},
		{"a reported lower bound past it", Progress{ContentBytes: 1, Reported: out(9)}, true},
		{"reasoning counts as output", Progress{Reported: reasoned}, true},
		{"a final count below the bytes", Progress{ContentBytes: 30, Reported: out(8), Final: 1 << catalog.ItemOutput}, false},
		{"a final count past it", Progress{Reported: out(9), Final: 1 << catalog.ItemOutput}, true},
		{"input never moves the meter", Progress{Reported: usage(t, map[catalog.Item]int64{in: 1e9}), Final: 1 << in}, false},
	} {
		if got := meter(t, Demand{TextBytes: 80}, entry(t, true, 3)).Observe(tc.p); got.Exhausted != tc.exhausted {
			t.Errorf("%s: Observe = %+v, want exhausted %v", tc.name, got, tc.exhausted)
		}
	}
	limited := entry(t, true, 3)
	limited.MaxOutput = 6
	if got := meter(t, Demand{TextBytes: 80}, limited).Observe(Progress{ContentBytes: 1000}); got.Extend || got.Exhausted {
		t.Errorf("past the output limit of 6: Observe = %+v, want to continue", got)
	}
}

// TestMeterToolCalls: the framework of a client tool call is output the upstream may omit from its report, so the
// meter's byte bound adds it too. Thirty calls of 45 tokens pass a segment of 8 by far even though the arguments are
// empty, and the extension they ask for covers the settlement's estimate rather than leaving the difference to the
// platform.
func TestMeterToolCalls(t *testing.T) {
	t.Parallel()

	e := entry(t, true, 3)
	e.MaxOutput = 4000
	m := meter(t, Demand{TextBytes: 80}, e)
	step := m.Observe(Progress{ContentBytes: 60, ToolCalls: 30, ToolCallTokens: 45})
	if !step.Extend || !step.Exhausted || step.Request.Seq != 2 || units(t, step.Request.Target) != target(t, 1418) {
		t.Fatalf("Observe = %+v, want extension 2 to %s and exhausted", step, target(t, 1418))
	}
	m.Extended(2, nil)
	if step := m.Observe(Progress{ContentBytes: 60, ToolCalls: 30, ToolCallTokens: 45}); step.Extend || step.Exhausted {
		t.Errorf("after approval: Observe = %+v, want to continue", step)
	}
	plain := meter(t, Demand{TextBytes: 80}, e)
	if step := plain.Observe(Progress{ContentBytes: 3}); step.Extend || step.Exhausted {
		t.Errorf("without the calls: Observe = %+v, want no extension", step)
	}
}

// TestMeterLeft: Left is what the approved segments still cover of the output, never below zero, and grows by the
// segment an extension adds; once they cover the output limit nothing more can come.
func TestMeterLeft(t *testing.T) {
	t.Parallel()

	m := meter(t, Demand{TextBytes: 80}, entry(t, true, 3))
	for _, tc := range []struct {
		bytes, left int64
	}{{0, 8}, {3, 5}, {8, 0}} {
		if got := m.Observe(Progress{ContentBytes: tc.bytes}).Left; got != tc.left {
			t.Errorf("after %d bytes: Left %d, want %d", tc.bytes, got, tc.left)
		}
	}
	m.Extended(2, nil) // asked for at 8 bytes: cover them and a segment, 16 tokens
	if got := m.Observe(Progress{ContentBytes: 9}).Left; got != 7 {
		t.Errorf("after the extension: Left %d, want 7", got)
	}
	limited := entry(t, true, 3)
	limited.MaxOutput = 6
	if got := meter(t, Demand{TextBytes: 80}, limited).Observe(Progress{ContentBytes: 6}).Left; got != math.MaxInt64 {
		t.Errorf("at the output limit: Left %d, want no bound", got)
	}
}

// TestMeterLeftNeedsAnExtension: whenever Left is 0 an extension is in flight (it was asked for in that step or before),
// whatever the read, so the owner that waits for one at Left 0 has one to wait for.
func TestMeterLeftNeedsAnExtension(t *testing.T) {
	t.Parallel()

	m := meter(t, Demand{TextBytes: 80}, entry(t, true, 3))
	for bytes := range int64(60) {
		if step := m.Observe(Progress{ContentBytes: bytes}); step.Left == 0 && !m.flying {
			t.Fatalf("after %d bytes: Left 0 with no extension in flight, %+v", bytes, step)
		}
	}
}

// TestMeterFreeOutput: output that costs nothing spends no segment however much of it there is, so no step is Exhausted,
// no extension is due, and Left is unbounded. Left of 0 means an extension is due or in flight, which the owner waits
// for; with nothing to ask for, it would wait on none, for ever.
func TestMeterFreeOutput(t *testing.T) {
	t.Parallel()

	free := entry(t, true, 3)
	free.Price = mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"1","output":"0"}}`)
	m := meter(t, Demand{TextBytes: 80}, free)
	for _, bytes := range []int64{0, 4, 8, 9, 100, 5000} {
		if step := m.Observe(Progress{ContentBytes: bytes}); step != (MeterStep{Left: math.MaxInt64}) {
			t.Errorf("after %d bytes: %+v, want to read on, unbounded", bytes, step)
		}
	}
}

// TestMeterLargeEvent: one read of 30 bytes passes the extension point and the whole segment at once, so the step
// carries both; the request covers the 30 tokens already read plus a segment, so once approved the meter continues.
func TestMeterLargeEvent(t *testing.T) {
	t.Parallel()

	m := meter(t, Demand{TextBytes: 80}, entry(t, true, 3))
	step := m.Observe(Progress{PartialBytes: 30})
	if !step.Extend || !step.Exhausted || step.Request.Seq != 2 || units(t, step.Request.Target) != target(t, 38) {
		t.Fatalf("Observe = %+v, want extension 2 to %s and exhausted", step, target(t, 38))
	}
	m.Extended(2, nil)
	if step := m.Observe(Progress{PartialBytes: 30}); step.Extend || step.Exhausted {
		t.Errorf("after approval: Observe = %+v, want to continue", step)
	}
}

// TestMeterOneExtensionInFlight: no second request while one is in flight, even as the segment runs out; a failed
// extension leaves the approved segments alone and the next Observe asks for the same segment again.
func TestMeterOneExtensionInFlight(t *testing.T) {
	t.Parallel()

	m := meter(t, Demand{TextBytes: 80}, entry(t, true, 3))
	first := m.Observe(Progress{ContentBytes: 5})
	if !first.Extend || first.Request.Seq != 2 || units(t, first.Request.Target) != target(t, 16) {
		t.Fatalf("Observe = %+v, want extension 2 to %s", first, target(t, 16))
	}
	for _, n := range []int64{7, 9} {
		if step := m.Observe(Progress{ContentBytes: n}); step.Extend || step.Exhausted != (n > 8) {
			t.Fatalf("in flight at %d: Observe = %+v, want no extension", n, step)
		}
	}
	m.Extended(2, errors.New("database timeout"))
	if step := m.Observe(Progress{ContentBytes: 9}); !step.Extend || !step.Exhausted || step.Request != first.Request {
		t.Errorf("after a failure: Observe = %+v, want the same request %+v again and exhausted", step, first.Request)
	}
}

// TestMeterLostAcknowledgement: the first request for segment 2 commits but its acknowledgement is lost while the
// output grows past what it asked for. The resent request is the same, so Extend's "already segment 2" success
// approves exactly what was committed; the further need goes to segment 3.
func TestMeterLostAcknowledgement(t *testing.T) {
	t.Parallel()

	m := meter(t, Demand{TextBytes: 80}, entry(t, true, 3))
	first := m.Observe(Progress{ContentBytes: 5})
	m.Extended(2, errors.New("connection reset after commit"))
	resent := m.Observe(Progress{ContentBytes: 13})
	if !resent.Extend || resent.Request != first.Request {
		t.Fatalf("resent = %+v, want %+v", resent, first.Request)
	}
	m.Extended(2, nil) // Extend found segment 2 already written
	step := m.Observe(Progress{ContentBytes: 13})
	if step.Exhausted || !step.Extend || step.Request.Seq != 3 || units(t, step.Request.Target) != target(t, 24) {
		t.Errorf("after success: Observe = %+v, want extension 3 to %s", step, target(t, 24))
	}
}

// TestMeterTriggerPoint: the drawn per mille maps to a point rounded down within the 8-token segment, so 40% (3.2)
// extends past 3 tokens and 60% (4.8) past 4: early, never late.
func TestMeterTriggerPoint(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ trigger, last, first int64 }{{400, 3, 4}, {600, 4, 5}} {
		m := meter(t, Demand{TextBytes: 80}, entry(t, true, 3))
		m.trigger = tc.trigger
		m.limits()
		if step := m.Observe(Progress{ContentBytes: tc.last}); step.Extend {
			t.Errorf("trigger %d at %d tokens: Observe = %+v, want to continue", tc.trigger, tc.last, step)
		}
		if step := m.Observe(Progress{ContentBytes: tc.first}); !step.Extend {
			t.Errorf("trigger %d at %d tokens: Observe = %+v, want an extension", tc.trigger, tc.first, step)
		}
	}
}

// TestMeterSearches: searches up to the quote's most uses are reserved already; beyond them they are spent like
// output, counted by event or by the reported count, whichever is higher.
func TestMeterSearches(t *testing.T) {
	t.Parallel()

	d := Demand{TextBytes: 80, ServerTools: []ServerTool{{Item: webSearch, MaxUses: 1}}}
	e := entry(t, true, 3)
	m := meter(t, d, e)
	if step := m.Observe(Progress{Searches: 1}); step.Extend || step.Exhausted {
		t.Fatalf("one search: Observe = %+v, want to continue", step)
	}
	step := m.Observe(Progress{Searches: 2, Reported: usage(t, map[catalog.Item]int64{webSearch: 3})})
	// The fixed part holds 80 + the quoted search's sampling (1000 at 4) + its fee (100), the allowance 150 at 20; the
	// request adds 16 output tokens at 20 and, for each of the 2 searches beyond, a sampling (1000 at 4) and a fee.
	want := units(t, amount(t, 80+4000+100+150*20+16*20+2*(4000+100)))
	if !step.Extend || !step.Exhausted || units(t, step.Request.Target) != want {
		t.Fatalf("3 searches: Observe = %+v, want extension to %s and exhausted", step, want)
	}
	// The dearest call the approved segments allow, priced per sampling: the first prompt, one sampling per search
	// rereading the whole context, the 3 searches and 16 output tokens.
	samplings := []map[catalog.Item]int64{{in: 80}, {in: 1000, webSearch: 3}, {in: 1000}, {in: 1000, catalog.ItemOutput: 16}}
	cost := sum()
	for _, c := range samplings {
		got, missing, err := e.Price.Cost(usage(t, c))
		if err != nil || missing != 0 {
			t.Fatal(missing, err)
		}
		cost = sum(cost, got)
	}
	if step.Request.Target.Cmp(cost) < 0 {
		t.Errorf("target %s below the call's cost %s", want, units(t, cost))
	}
	// With compaction every search adds two samplings: the quote's fixed part rereads three contexts, each search
	// beyond adds two more.
	d.Compaction = true
	step = meter(t, d, e).Observe(Progress{Searches: 3})
	if want := units(t, amount(t, 80+3*4000+100+150*20+16*20+2*(2*4000+100))); units(t, step.Request.Target) != want {
		t.Errorf("3 searches with compaction: Observe = %+v, want extension to %s", step, want)
	}
}

// TestMeterFull: an entry reserved in full is never extended or truncated.
func TestMeterFull(t *testing.T) {
	t.Parallel()

	if step := meter(t, Demand{TextBytes: 80}, entry(t, false, 3)).Observe(Progress{ContentBytes: 1e6, Searches: 50}); step != (MeterStep{Left: math.MaxInt64}) {
		t.Errorf("Observe = %+v, want to continue", step)
	}
}

func TestBindDroppedEntry(t *testing.T) {
	t.Parallel()

	r, err := Quote(Demand{TextBytes: 80}, []Quotable{entry(t, true, 3), entry(t, false, 3)}, segments,
		[]Available{{SourceBalance, amount(t, 2000)}})
	if err != nil || len(r.Kept) != 1 {
		t.Fatalf("Quote = %+v, %v; want the second entry dropped", r, err)
	}
	defer func() {
		if recover() == nil {
			t.Error("Bind of a dropped entry did not panic")
		}
	}()
	r.Bind(1)
}

// TestQuoteTrigger: the draw stays within 40% to 60% of a segment; where each value lands is TestMeterTriggerPoint's.
func TestQuoteTrigger(t *testing.T) {
	t.Parallel()

	for range 1000 {
		r, err := Quote(Demand{}, []Quotable{entry(t, true, 3)}, segments, nil)
		if err != nil || r.trigger < 400 || r.trigger > 600 {
			t.Fatalf("Quote = %+v, %v; want a trigger from 400 to 600 per mille", r, err)
		}
	}
}

// BenchmarkObserve: Observe runs after every read of every stream, so it must not allocate; the benchmark fails if it
// does.
func BenchmarkObserve(b *testing.B) {
	m := meter(b, Demand{TextBytes: 80, ServerTools: []ServerTool{{Item: webSearch, MaxUses: 1}}}, entry(b, true, 3))
	p := Progress{ContentBytes: 3, Searches: 1, Reported: usage(b, map[catalog.Item]int64{in: 80, catalog.ItemOutput: 1}), Final: 1 << in}
	observe := func() {
		if step := m.Observe(p); step.Extend || step.Exhausted {
			b.Fatalf("Observe = %+v", step)
		}
	}
	if allocs := testing.AllocsPerRun(100, observe); allocs != 0 {
		b.Fatalf("Observe allocates %v times per call, want 0", allocs)
	}
	b.ReportAllocs()
	for b.Loop() {
		observe()
	}
}
