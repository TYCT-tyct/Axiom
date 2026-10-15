package billing

import (
	"bytes"
	"errors"
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

// TestMeterLargeEvent: one read of 30 bytes passes the extension point and the whole segment at once, so the step
// carries both; the request covers the 30 tokens already read plus a segment, so once approved the meter continues.
func TestMeterLargeEvent(t *testing.T) {
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
	m := meter(t, Demand{TextBytes: 80}, entry(t, true, 3))
	if step := m.Observe(Progress{ContentBytes: 5}); !step.Extend || step.Request.Seq != 2 {
		t.Fatalf("Observe = %+v, want extension 2", step)
	}
	for _, n := range []int64{7, 9} {
		if step := m.Observe(Progress{ContentBytes: n}); step.Extend || step.Exhausted != (n > 8) {
			t.Fatalf("in flight at %d: Observe = %+v, want no extension", n, step)
		}
	}
	m.Extended(2, errors.New("database timeout"))
	step := m.Observe(Progress{ContentBytes: 9})
	if !step.Extend || !step.Exhausted || step.Request.Seq != 2 || units(t, step.Request.Target) != target(t, 17) {
		t.Errorf("after a failure: Observe = %+v, want extension 2 to %s and exhausted", step, target(t, 17))
	}
}

// TestMeterSearches: searches up to the quote's most uses are reserved already; beyond them they are spent like
// output, counted by event or by the reported count, whichever is higher.
func TestMeterSearches(t *testing.T) {
	d := Demand{TextBytes: 80, ServerTools: []ServerTool{{Item: webSearch, MaxUses: 1}}}
	m := meter(t, d, entry(t, true, 3))
	if step := m.Observe(Progress{Searches: 1}); step.Extend || step.Exhausted {
		t.Fatalf("one search: Observe = %+v, want to continue", step)
	}
	reported := usage(t, map[catalog.Item]int64{webSearch: 3})
	step := m.Observe(Progress{Searches: 2, Reported: reported})
	// The fixed part also holds the second sampling's input (1000 at 4) and the quoted search (100); output at 20.
	want := units(t, sum(amount(t, 80+4000+100+150*20+16*20), amount(t, 2*100)))
	if !step.Extend || !step.Exhausted || units(t, step.Request.Target) != want {
		t.Errorf("3 searches: Observe = %+v, want extension to %s and exhausted", step, want)
	}
}

// TestMeterFull: an entry reserved in full is never extended or truncated.
func TestMeterFull(t *testing.T) {
	if step := meter(t, Demand{TextBytes: 80}, entry(t, false, 3)).Observe(Progress{ContentBytes: 1e6, Searches: 50}); step != (MeterStep{}) {
		t.Errorf("Observe = %+v, want to continue", step)
	}
}

func TestBindDroppedEntry(t *testing.T) {
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

// TestQuoteTrigger: the extension point is drawn once per request from 40% to 60% of a segment.
func TestQuoteTrigger(t *testing.T) {
	seen := map[int64]bool{}
	for range 2000 {
		r, err := Quote(Demand{}, []Quotable{entry(t, true, 3)}, segments, nil)
		if err != nil || r.trigger < 400 || r.trigger > 600 {
			t.Fatalf("Quote = %+v, %v; want a trigger from 400 to 600 per mille", r, err)
		}
		seen[r.trigger] = true
	}
	if !seen[400] || !seen[600] || len(seen) < 150 {
		t.Errorf("2000 quotes drew %d distinct triggers, ends %v %v; want them spread from 400 to 600", len(seen), seen[400], seen[600])
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
