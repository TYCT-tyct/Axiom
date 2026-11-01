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
// TestQuoteCompletionsReservesEveryChoice: a legacy completion's whole output bound is reserved at once — every prompt
// and every n choice — and an explicit max_tokens 0 reserves no output at all. Routing marks such an entry
// non-segmentable; these amounts are what that decision buys.
func TestQuoteCompletionsReservesEveryChoice(t *testing.T) {
	t.Parallel()
	e := entry(t, false, 0)
	e.MaxOutput, e.Model.ContextLength = 4096, 1<<20
	d := Demand{TextBytes: 100, InputBytes: 100, Choices: 2}
	q, err := Quote(d, []Quotable{e}, Segments{}, []Available{{SourceBalance, amount(t, 1e9)}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := units(t, q.Amount), "82020"; got != want { // 100 input + 2×4096 output at 10
		t.Errorf("two choices reserve %s, want %s", got, want)
	}
	e.MaxOutput = 0
	q, err = Quote(Demand{TextBytes: 100, InputBytes: 100}, []Quotable{e}, Segments{}, []Available{{SourceBalance, amount(t, 1e9)}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := units(t, q.Amount), "100"; got != want {
		t.Errorf("an explicit zero cap reserves %s, want only the input %s", got, want)
	}
}

func TestQuoteEntry(t *testing.T) {
	t.Parallel()

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
		{"a flat search adds only its fee: 80 + 100 + 500 at 10", tools(text, ServerTool{Item: webSearch, MaxUses: 1, Flat: true}), false, "5180"},
		{"two flat parses: 80 + 2×100 + 500 at 10", tools(text, ServerTool{Item: webSearch, MaxUses: 2, Flat: true}), false, "5280"},
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
		got, err := quoteEntry(tc.d, entry(t, tc.segmentable, 3), segments, false)
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
	t.Parallel()

	noPageLimit := entry(t, false, 3)
	noPageLimit.Model.FilePages = 0
	dearCache := Quotable{
		Price:     mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"1","cache_write":"1","cache_read":"2","output":"1"}}`),
		Model:     Model{ContextLength: 1000, Reports: 1<<in | 1<<catalog.ItemCacheRead | 1<<catalog.ItemOutput},
		MaxOutput: 1,
	}
	standardOnly := entry(t, false, 3)
	standardOnly.Model.ServiceTiers = 1 << catalog.ServiceTierStandard
	for _, tc := range []struct {
		name string
		d    Demand
		e    Quotable
		want string
	}{
		{
			"a request that may be served at priority, to an endpoint that offers standard only: 80 at 1 + 500 at 10",
			Demand{TextBytes: 80, ServiceTiers: 1<<catalog.ServiceTierStandard | 1<<catalog.ServiceTierPriority},
			standardOnly, "5080",
		},
		{"a file without a page limit: 1000 at 4 + 500 at 20", Demand{Documents: 1}, noPageLimit, "14000"},
		{"cache reads dearer than input: 80 at 2 + 1", Demand{TextBytes: 80}, dearCache, "161"},
		{
			"web search with a limit of its own on a model with no default: 80 + 1000 at 4 + 100 + 500 at 20",
			Demand{TextBytes: 80, ServerTools: []ServerTool{{Item: webSearch, MaxUses: 1}}},
			entry(t, false, 0), "14180",
		},
	} {
		got, err := quoteEntry(tc.d, tc.e, segments, false)
		if err != nil || units(t, got.amount) != tc.want {
			t.Errorf("%s: quote = %s, %v; want %s", tc.name, units(t, got.amount), err, tc.want)
		}
	}
}

func TestQuoteEntryRefuses(t *testing.T) {
	t.Parallel()

	noContext := entry(t, false, 3)
	noContext.Model.ContextLength = 0
	noCodeDefault := entry(t, false, 3)
	noCodeDefault.Model.MaxUses = map[catalog.Item]int64{webSearch: 3}
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
		{"a search with no limit of its own, on a model with no default", Demand{ServerTools: []ServerTool{{Item: webSearch}}}, entry(t, false, 0)},
		{"a code execution with no limit of its own, on a model with none by default", Demand{ServerTools: []ServerTool{{Item: codeExec}}}, noCodeDefault},
		{"a model without a context length", Demand{}, noContext},
		{"an output limit past int64 behind a small first segment", Demand{Choices: 2}, hugeOutput},
	} {
		if got, err := quoteEntry(tc.d, tc.e, segments, false); err == nil {
			t.Errorf("%s: quote = %s, want an error", tc.name, units(t, got.amount))
		}
	}
}

// TestQuote: the reservation is the highest quote among the entries every source can afford; the entries are kept in
// route order; a dropped preferred entry refuses the request, naming the first source short. Entries differ in their
// model's default searches (3 and 1), so a route across models quotes each by its own.
func TestQuote(t *testing.T) {
	t.Parallel()

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
		{"preferred dropped", []Quotable{threeFull, threeSegmented}, []Available{balance(17000)}, "", nil, &Insufficient{Source: SourceBalance}},
		{
			"the first source short",
			[]Quotable{oneFull},
			[]Available{balance(1e6), {SourceSiteBalance, amount(t, 1e6)}, {SourceKey, amount(t, 100)}, {SourceWorkspace, amount(t, 1)}},
			"", nil, &Insufficient{Source: SourceKey},
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

// TestQuoteRefusesAToolWithNoBound: a server tool the request enables without a limit, on a model with no default
// maximum, has no upper cost and so no reservation: Quote refuses it with ErrUnbounded, naming the tool, however the
// route's other entries quote. The same request with a limit of its own, or on a model with a default, is quoted.
func TestQuoteRefusesAToolWithNoBound(t *testing.T) {
	t.Parallel()

	d := func(max int64) Demand {
		return Demand{TextBytes: 80, ServerTools: []ServerTool{{Item: webSearch, MaxUses: max}}}
	}
	unbounded, bounded := entry(t, false, 0), entry(t, false, 3)
	avail := []Available{{SourceBalance, amount(t, 1e9)}}
	for _, tc := range []struct {
		name   string
		d      Demand
		route  []Quotable
		wantOK bool
	}{
		{"no limit, no default", d(0), []Quotable{unbounded}, false},
		{"no limit, no default on the second entry", d(0), []Quotable{bounded, unbounded}, false},
		{"a limit of its own", d(2), []Quotable{unbounded}, true},
		{"the model's default", d(0), []Quotable{bounded}, true},
	} {
		_, err := Quote(tc.d, tc.route, segments, avail)
		if tc.wantOK != (err == nil) || (!tc.wantOK && !errors.Is(err, ErrUnbounded)) {
			t.Errorf("%s: Quote = %v, want success %v (ErrUnbounded when refused)", tc.name, err, tc.wantOK)
		}
	}
}

// TestQuoteEntryNeedsSegments: a segmentable entry with no segment size or no overshoot configured is refused, not
// quoted with a segment of 0 tokens, which reserves no output; the same entry whole needs neither.
func TestQuoteEntryNeedsSegments(t *testing.T) {
	t.Parallel()

	d := Demand{TextBytes: 80}
	for name, s := range map[string]Segments{
		"no segment size": {Overshoot: time.Second},
		"no overshoot":    {Output: 40},
		"neither":         {},
	} {
		if got, err := quoteEntry(d, entry(t, true, 3), s, false); !errors.Is(err, errNoSegments) {
			t.Errorf("%s: quote = %s, %v; want errNoSegments", name, units(t, got.amount), err)
		}
		if _, err := Quote(d, []Quotable{entry(t, true, 3)}, s, nil); !errors.Is(err, errNoSegments) {
			t.Errorf("%s: Quote = %v; want errNoSegments", name, err)
		}
	}
	if got, err := quoteEntry(d, entry(t, false, 3), Segments{}, false); err != nil || units(t, got.amount) != "5080" {
		t.Errorf("a whole entry with no segments: quote = %s, %v; want 5080", units(t, got.amount), err)
	}
}

func TestReservedCostEstimatesMissingPricesWithoutChangingAdmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, rates, balance string
		openAI               bool
		reports              catalog.ItemSet
		tools                []ServerTool
	}{
		{"missing tier", `"input":"1","cache_read":"1","output":"2"`, "22", false, 1 << catalog.ItemCacheRead, nil},
		{"overlapping caches", `"input":"1","cache_read":"1","output":"2"`, "24", true, 1<<catalog.ItemCacheRead | 1<<catalog.ItemCacheWrite, nil},
		{"known tool stays dearer", `"input":"1","output":"2","web_search":"5"`, "227", false, 0, []ServerTool{{Item: catalog.ItemWebSearch, MaxUses: 1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := Quotable{
				Price: mustSchedule(t, `{"v":1,"currency":"USD","rates":{`+tc.rates+`}}`), MaxOutput: 10,
				Model: Model{ContextLength: 100, Reports: tc.reports, OpenAIUsage: tc.openAI, ServiceTiers: 1<<catalog.ServiceTierStandard | 1<<catalog.ServiceTierPriority},
			}
			d := Demand{TextBytes: 1, ServiceTiers: ^catalog.ServiceTierSet(0), ServerTools: tc.tools}
			cost, err := ReservedCost(d, e)
			want, parseErr := platform.ParseExact(tc.balance)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if err != nil || cost.Cmp(want) != 0 {
				t.Fatalf("native estimate = %v, %v, want %v", cost, err, want)
			}
			if _, err := Quote(d, []Quotable{e}, Segments{}, nil); !errors.Is(err, ErrUnpriced) {
				t.Fatalf("sale admission changed its missing-price rule: %v", err)
			}
		})
	}
}

// TestQuoteDirectBatchCrossesATier: the upstream reports one input total for a direct batch, which the settlement bands
// as one prompt, so the quote bands the batch's total too: ten 40-byte inputs that report 101 tokens settle above the
// 100-token threshold, and the reservation must cover that, not price each input at the base band.
func TestQuoteDirectBatchCrossesATier(t *testing.T) {
	t.Parallel()
	schedule := mustSchedule(t, prices)
	e := Quotable{Price: schedule, Model: Model{ContextLength: 1000}}
	q, err := quoteEntry(Demand{Direct: true, TextBytes: 400}, e, Segments{}, true)
	if err != nil {
		t.Fatalf("quoteEntry: %v", err)
	}
	reported, err := catalog.NewUsage(map[catalog.Item]int64{in: 101}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	settled, missing, err := schedule.Cost(reported)
	if err != nil || missing != 0 {
		t.Fatalf("Cost = %v, %v, %v", settled, missing, err)
	}
	if units(t, settled) != "404" || q.amount.Cmp(settled) < 0 {
		t.Errorf("reserved %s for a batch that settles at %s; want 404 settled within the reservation", units(t, q.amount), units(t, settled))
	}
}

// TestQuoteRerankSearchCrossesATier: a rerank's search unit is priced at the band its batch's total reaches, not at
// the context: each document fits a 4096-token context, the batch reports 8000 input tokens and one search unit, and the
// settlement prices that unit at the tier above 5000 tokens, which the reservation must cover.
func TestQuoteRerankSearchCrossesATier(t *testing.T) {
	t.Parallel()
	schedule := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0","web_search":"1"},
		"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":5000,"rates":{"input":"0","web_search":"10"}}]}}`)
	e := Quotable{Price: schedule, Model: Model{ContextLength: 4096}}
	q, err := quoteEntry(Demand{Direct: true, TextBytes: 32000, ServerTools: []ServerTool{{Item: webSearch, MaxUses: 1}}}, e, Segments{}, true)
	if err != nil {
		t.Fatalf("quoteEntry: %v", err)
	}
	reported, err := catalog.NewUsage(map[catalog.Item]int64{in: 8000, webSearch: 1}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	settled, missing, err := schedule.Cost(reported)
	if err != nil || missing != 0 {
		t.Fatalf("Cost = %v, %v, %v", settled, missing, err)
	}
	if units(t, settled) != "10" || q.amount.Cmp(settled) < 0 {
		t.Errorf("reserved %s for a rerank that settles at %s; want 10 settled within the reservation", units(t, q.amount), units(t, settled))
	}
}

// TestQuoteDirect: an embeddings or rerank call has no output to reserve and no second sampling, even at an endpoint
// whose model also generates and so has an output limit. Its schedule needs only the input price, and rerank's search
// unit is a flat fee, not a whole-context resample.
func TestQuoteDirect(t *testing.T) {
	t.Parallel()
	schedule := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"1","web_search":"1"}}`)
	e := Quotable{Price: schedule, Model: Model{ContextLength: 1000}, MaxOutput: 100}
	d := Demand{Direct: true, TextBytes: 2, ServerTools: []ServerTool{{Item: webSearch, MaxUses: 1}}}
	q, err := quoteEntry(d, e, Segments{}, true)
	if err != nil {
		t.Fatalf("quoteEntry: %v", err)
	}
	if got := units(t, q.amount); got != "3" {
		t.Errorf("amount = %s, want 3 (2 input + 1 search), not a whole-context resample", got)
	}

	d = Demand{Direct: true, TextBytes: 12000}
	first, longest := InputBound(d, Model{ContextLength: 8192})
	if first != 12000 || longest != 12000 {
		t.Errorf("InputBound = %d, %d; want the batch total 12000 for both, past the 8192-token context", first, longest)
	}
	if got := InputEstimate(d, Model{ContextLength: 8192}); got != 3000 {
		t.Errorf("InputEstimate = %d, want the batch total 3000 tokens", got)
	}
}

// TestQuoteMixedSearchKinds: a request that enables both the gateway's flat web plugin and a native search tool
// reserves a sampling for each native use, charges both fees, and does not let the flat search hide an unbounded
// native tool. The flat fee alone does not stand in for a native search in the meter's reserved count.
func TestQuoteMixedSearchKinds(t *testing.T) {
	t.Parallel()

	d := Demand{TextBytes: 80, ServerTools: []ServerTool{
		{Item: webSearch, MaxUses: 1},
		{Item: webSearch, MaxUses: 1, Flat: true},
	}}
	q, err := quoteEntry(d, entry(t, false, 3), Segments{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := units(t, q.amount), "14280"; got != want {
		t.Errorf("mixed search quote = %s, want %s", got, want)
	}
	if q.searches != 1 {
		t.Errorf("reserved native searches = %d, want 1 (the flat search is not a native use)", q.searches)
	}
	if q.resample == (inputPrice{}) || q.rereads != 1000 {
		t.Errorf("native resampling was cleared by the flat search: resample %+v, rereads %d", q.resample, q.rereads)
	}

	unboundedDemand := Demand{TextBytes: 80, ServerTools: []ServerTool{
		{Item: webSearch},
		{Item: webSearch, MaxUses: 1, Flat: true},
	}}
	_, err = quoteEntry(unboundedDemand, entry(t, false, 0), Segments{}, false) // no default native limit
	var unbounded *UnboundedError
	if !errors.As(err, &unbounded) || !errors.Is(err, ErrUnbounded) {
		t.Errorf("unbounded native search with a flat search: error = %v, want ErrUnbounded", err)
	}

	m := meter(t, d, entry(t, true, 3))
	if step := m.Observe(Progress{Searches: 1}); step.Extend {
		t.Errorf("one native search = %+v, want no extension", step)
	}
	if step := m.Observe(Progress{Searches: 2}); !step.Extend {
		t.Errorf("a native search beyond its bound = %+v, want an extension", step)
	}
}
