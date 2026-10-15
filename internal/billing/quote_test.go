package billing

import (
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform"
)

// prices: a context tier above 100 input tokens and a priority service tier, in whole currency units so every quote
// below can be checked by hand.
const prices = `{"v":1,"currency":"USD",
	"rates":{"input":"1","cache_write":"2","cache_write_1h":"3","output":"10","web_search":"100","code_execution":"50"},
	"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":100,"rates":{"input":"4","output":"20"}}]},
	"service_tiers":{"priority":{"context_tiers":[{"above":100,"rates":{}}],"rates":{"input":"6","output":"30"}}}}`

const (
	in, cacheWrite, cacheWrite1h = catalog.ItemInput, catalog.ItemCacheWrite, catalog.ItemCacheWrite1h
	webSearch, codeExec          = catalog.ItemWebSearch, catalog.ItemCodeExecution
)

func mustSchedule(tb testing.TB, s string) catalog.Schedule {
	tb.Helper()
	schedule, err := catalog.ParseSchedule([]byte(s))
	if err != nil {
		tb.Fatal(err)
	}
	return schedule
}

// entry is a route entry on prices: a 1000-token context, 50 tokens an image, 200 a page and 3 pages a file, 100
// tokens a second, and by default three web searches and two code executions.
func entry(tb testing.TB, segmentable bool, searches int64) Quotable {
	tb.Helper()
	return Quotable{
		Price: mustSchedule(tb, prices),
		Model: Model{
			ContextLength: 1000, ImageTokens: 50, PageTokens: 200, FilePages: 3, OutputSpeed: 100,
			MaxUses: map[catalog.Item]int64{webSearch: searches, codeExec: 2},
		},
		MaxOutput: 500, Segmentable: segmentable,
	}
}

// segments: 40 output tokens a segment and 1.5s of overshoot, 150 tokens at 100 tokens a second.
var segments = Segments{Output: 40, Overshoot: 1500 * time.Millisecond}

func units(tb testing.TB, e platform.Exact) string {
	tb.Helper()
	return e.Rat().RatString()
}

// TestQuoteEntry: each rule of docs/domains/billing.md「预留与结算」 moves the quote by the amount worked out beside it.
func TestQuoteEntry(t *testing.T) {
	text := Demand{TextBytes: 80}
	tools := func(d Demand, ts ...ServerTool) Demand { d.ServerTools = ts; return d }
	for _, tc := range []struct {
		name        string
		d           Demand
		segmentable bool
		want        string
	}{
		{"text, full: 80 input + 500 output at 10", text, false, "5080"},
		{"text, first segment: 80 + 40 output + 150 allowance at 10", text, true, "1980"},
		{"images take the prompt over the tier: 110 at 4 + 500 at 20", Demand{TextBytes: 10, Images: 2}, false, "10440"},
		{"a file of 3 pages at 200 tokens: 600 at 4 + 500 at 20", Demand{Documents: 1}, false, "12400"},
		{"the context length caps the prompt: 1000 at 4 + 500 at 20", Demand{TextBytes: 5000}, false, "14000"},
		{"the 1-hour write price bounds the input: 80 at 3", Demand{TextBytes: 80, InputItems: 1 << cacheWrite1h}, false, "5240"},
		{"the dearer service tier: 80 at 6 + 500 at 30", Demand{TextBytes: 80, ServiceTiers: 1 << catalog.ServiceTierPriority}, false, "15480"},
		{"two choices double the output: 80 + 1000 at 10", Demand{TextBytes: 80, Choices: 2}, false, "10080"},
		{"model default of 3 searches: 80 + 3×1000 at 4 + 300 + 500 at 20", tools(text, ServerTool{Item: webSearch}), false, "22380"},
		{"the request's 1 search: 80 + 1000 at 4 + 100 + 500 at 20", tools(text, ServerTool{Item: webSearch, MaxUses: 1}), false, "14180"},
		{"compaction doubles 1 sampling: 80 + 1000 at 4 + 500 at 20", Demand{TextBytes: 80, Compaction: true}, false, "14080"},
		{
			"compaction doubles 2 samplings: 80 + 3×1000 at 4 + 100 + 500 at 20",
			tools(Demand{TextBytes: 80, Compaction: true}, ServerTool{Item: webSearch, MaxUses: 1}), false, "22180",
		},
		{
			"two tools: 80 + 3×1000 at 4 + 100 + 2×50 + 500 at 20",
			tools(text, ServerTool{Item: webSearch, MaxUses: 1}, ServerTool{Item: codeExec}), false, "22280",
		},
		{
			"a tool listed twice adds up: 80 + 2×1000 at 4 + 200 + 500 at 20",
			tools(text, ServerTool{Item: webSearch, MaxUses: 1}, ServerTool{Item: webSearch, MaxUses: 1}), false, "18280",
		},
		{
			"segmented with a search: 80 + 1000 at 4 + 100 + (40 + 150) at 20",
			tools(text, ServerTool{Item: webSearch, MaxUses: 1}), true, "7980",
		},
	} {
		got, err := quoteEntry(tc.d, entry(t, tc.segmentable, 3), segments)
		if err != nil || units(t, got.amount) != tc.want {
			t.Errorf("%s: quote = %s, %v; want %s", tc.name, units(t, got.amount), err, tc.want)
		}
	}
}

// TestQuoteEntryModel: what the model data adds to the bound. A file whose page limit is unknown takes the whole
// context. A model that may report cache reads has its input bounded by the cache-read price as well: the review's
// counter-example prices a cache read above input, so 80 tokens read from the cache plus 1 output token cost 161,
// which a quote at the input price (81) would not cover.
func TestQuoteEntryModel(t *testing.T) {
	noPageLimit := entry(t, false, 3)
	noPageLimit.Model.FilePages = 0
	dearCache := Quotable{
		Price:     mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"1","cache_write":"1","cache_read":"2","output":"1"}}`),
		Model:     Model{ContextLength: 1000, Reports: 1<<in | 1<<catalog.ItemCacheRead | 1<<catalog.ItemOutput},
		MaxOutput: 1,
	}
	for _, tc := range []struct {
		name string
		d    Demand
		e    Quotable
		want string
	}{
		{"a file without a page limit: 1000 at 4 + 500 at 20", Demand{Documents: 1}, noPageLimit, "14000"},
		{"cache reads dearer than input: 80 at 2 + 1", Demand{TextBytes: 80}, dearCache, "161"},
		{
			"web search listed with no uses may still search, so output at the context's band: 80 + 500 at 20",
			Demand{TextBytes: 80, ServerTools: []ServerTool{{Item: webSearch}}},
			entry(t, false, 0), "10080",
		},
	} {
		got, err := quoteEntry(tc.d, tc.e, segments)
		if err != nil || units(t, got.amount) != tc.want {
			t.Errorf("%s: quote = %s, %v; want %s", tc.name, units(t, got.amount), err, tc.want)
		}
	}
}

func TestQuoteEntryRefuses(t *testing.T) {
	noContext := entry(t, false, 3)
	noContext.Model.ContextLength = 0
	hugeOutput := entry(t, true, 3)
	hugeOutput.MaxOutput = math.MaxInt64/2 + 1
	for _, tc := range []struct {
		name string
		d    Demand
		e    Quotable
	}{
		{"a service tier the schedule does not offer", Demand{ServiceTiers: 1 << catalog.ServiceTierFlex}, entry(t, false, 3)},
		{"an input item it does not price", Demand{InputItems: 1 << catalog.ItemAudioIn}, entry(t, false, 3)},
		{
			"a tool the priority tier does not price",
			Demand{ServiceTiers: 1 << catalog.ServiceTierPriority, ServerTools: []ServerTool{{Item: webSearch}}},
			entry(t, false, 3),
		},
		{"a model without a context length", Demand{}, noContext},
		{"an output limit past int64 behind a small first segment", Demand{Choices: 2}, hugeOutput},
	} {
		if got, err := quoteEntry(tc.d, tc.e, segments); err == nil {
			t.Errorf("%s: quote = %s, want an error", tc.name, units(t, got.amount))
		}
	}
}

// TestQuote: the reservation is the highest quote among the entries every source can afford; the entries are kept in
// route order; a dropped preferred entry refuses the request, naming the first source short. Entries differ in their
// model's default searches (3 and 1), so a route across models quotes each by its own.
func TestQuote(t *testing.T) {
	d := Demand{TextBytes: 80, ServerTools: []ServerTool{{Item: webSearch}}}
	threeSegmented, threeFull, oneFull := entry(t, true, 3), entry(t, false, 3), entry(t, false, 1) // 16180, 22380, 14180
	balance := func(n int64) Available { return Available{SourceBalance, amount(t, n)} }
	for _, tc := range []struct {
		name   string
		route  []Quotable
		avail  []Available
		want   string
		kept   []int
		refuse *Insufficient
	}{
		{"the highest quote", []Quotable{threeSegmented, oneFull, threeFull}, []Available{balance(1e6)}, "22380", []int{0, 1, 2}, nil},
		{"an amount equal to the quote suffices", []Quotable{oneFull}, []Available{balance(14180)}, "14180", []int{0}, nil},
		{"unaffordable entries dropped", []Quotable{threeSegmented, threeFull, oneFull}, []Available{balance(17000)}, "16180", []int{0, 2}, nil},
		{"only the segment affordable", []Quotable{threeSegmented, threeFull}, []Available{balance(16180)}, "16180", []int{0}, nil},
		{"preferred dropped", []Quotable{threeFull, threeSegmented}, []Available{balance(17000)}, "", nil, &Insufficient{SourceBalance}},
		{
			"the first source short",
			[]Quotable{oneFull},
			[]Available{balance(1e6), {SourceSiteBalance, amount(t, 1e6)}, {SourceKey, amount(t, 100)}, {SourceWorkspace, amount(t, 1)}},
			"", nil, &Insufficient{SourceKey},
		},
	} {
		r, err := Quote(d, tc.route, segments, tc.avail)
		var refused *Insufficient
		switch {
		case tc.refuse != nil:
			if !errors.As(err, &refused) || *refused != *tc.refuse {
				t.Errorf("%s: Quote = %v, %v; want %v", tc.name, r, err, tc.refuse)
			}
		case err != nil || units(t, r.Amount) != tc.want || !slices.Equal(r.Kept, tc.kept):
			t.Errorf("%s: Quote = %v, %v; want %s keeping %v", tc.name, r, err, tc.want, tc.kept)
		}
	}
}

// amount is n whole currency units.
func amount(tb testing.TB, n int64) platform.Exact {
	tb.Helper()
	p, err := platform.ParseUnitPrice("1")
	if err != nil {
		tb.Fatal(err)
	}
	return p.Mul(n)
}

// BenchmarkQuote runs under the balance-row lock: one entry, and a three-entry route over a schedule with two context
// tiers and two service tiers, with a server tool and cache reads. Only Kept and the per-entry quotes allocate.
func BenchmarkQuote(b *testing.B) {
	tiered := entry(b, true, 3)
	tiered.Price = mustSchedule(b, `{"v":1,"currency":"USD",
		"rates":{"input":"1","cache_read":"0.1","cache_write":"2","output":"10","web_search":"100"},
		"context_tiers":{"basis":["input","cache_read"],"method":"whole",
			"tiers":[{"above":100,"rates":{"input":"4","output":"20"}},{"above":500,"rates":{"input":"5"}}]},
		"service_tiers":{"priority":{"context_tiers":[{"above":100,"rates":{}},{"above":500,"rates":{}}],
			"rates":{"input":"6","cache_read":"0.6","cache_write":"7","output":"30","web_search":"100"}}}}`)
	tiered.Model.Reports = 1<<in | 1<<catalog.ItemCacheRead
	full := tiered
	full.Segmentable = false
	d := Demand{
		TextBytes: 5000, Images: 2, Documents: 1, ServerTools: []ServerTool{{Item: webSearch}},
		InputItems: 1 << cacheWrite, ServiceTiers: 1 << catalog.ServiceTierPriority,
	}
	avail := []Available{{SourceBalance, amount(b, 1e9)}, {SourceKey, amount(b, 1e9)}}
	for _, bc := range []struct {
		name  string
		d     Demand
		route []Quotable
	}{
		{"one entry", Demand{TextBytes: 5000}, []Quotable{entry(b, true, 3)}},
		{"three entries, tiers and tools", d, []Quotable{tiered, full, tiered}},
	} {
		b.Run(bc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Quote(bc.d, bc.route, segments, avail); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
