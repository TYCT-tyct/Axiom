package catalog_test

import (
	"math/big"
	"slices"
	"testing"

	"axiom/internal/catalog"
)

func mustUsage(tb testing.TB, c counts) catalog.Usage {
	tb.Helper()
	return mustTierUsage(tb, catalog.ServiceTierStandard, c)
}

func mustTierUsage(tb testing.TB, st catalog.ServiceTier, c counts) catalog.Usage {
	tb.Helper()
	u, err := catalog.NewUsage(c, st)
	if err != nil {
		tb.Fatal(err)
	}
	return u
}

type counts = map[catalog.Item]int64

const (
	in, cacheRead, cacheWrite, cacheWrite1h = catalog.ItemInput, catalog.ItemCacheRead, catalog.ItemCacheWrite, catalog.ItemCacheWrite1h
	audioIn, imageIn, audioOut, imageOut    = catalog.ItemAudioIn, catalog.ItemImageIn, catalog.ItemAudioOut, catalog.ItemImageOut
	out, reasoning, webSearch               = catalog.ItemOutput, catalog.ItemReasoning, catalog.ItemWebSearch
	codeExec                                = catalog.ItemCodeExecution
)

func TestCost(t *testing.T) {
	// OpenAI's prompt caching sample: 12,000 input tokens, all of them written to the cache, map to cache_write alone
	// (README.md「计费项与用量」); the write price replaces the input price and the write counts toward the threshold.
	// The prices and the threshold are made up to put the sample right above it.
	openAI := `{"v":1,"currency":"USD",
		"rates":{"input":"0.000005","cache_read":"0.0000005","cache_write":"0.00000625","output":"0.00003"},
		"context_tiers":{"basis":["input","cache_read","cache_write"],"method":"whole",
			"tiers":[{"above":11999,"rates":{"cache_write":"0.0000125"}}]}}`
	steps := `{"v":1,"currency":"CNY","rates":{"input":"1"},
		"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":10,"rates":{"input":"2"}},{"above":20,"rates":{"input":"3"}}]}}`
	all := `{"v":1,"currency":"USD","rates":{"input":"1000","cache_read":"1000","cache_write":"1000",
		"cache_write_1h":"1000","audio_in":"1000","image_in":"1000","output":"1000","reasoning":"1000","audio_out":"1000",
		"image_out":"1000","web_search":"1000","code_execution":"1000"}}`
	// Gemini 2.5 Pro's tiers with an image price of its own, as Gemini 3.6 Flash prices images apart from text: the
	// image tokens split out of the prompt still count toward the 200k threshold. The image prices are made up.
	geminiImages := `{"v":1,"currency":"USD","rates":{"input":"0.00000125","image_in":"0.000002","output":"0.00001"},
		"context_tiers":{"basis":["input","cache_read","audio_in","image_in"],"method":"whole",
			"tiers":[{"above":200000,"rates":{"input":"0.0000025","image_in":"0.000004","output":"0.000015"}}]}}`
	// Audio in and out at prices of their own, as OpenAI's audio models have; the prices are made up.
	audio := `{"v":1,"currency":"USD","rates":{"input":"1","audio_in":"10","output":"2","audio_out":"20"}}`
	smallest := `{"v":1,"currency":"USD","rates":{"input":"0.000000000000001"}}`
	free := `{"v":1,"currency":"USD","rates":{"input":"1","cache_read":"0","output":"1"},
		"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":10,"rates":{"output":"0"}}]}}`
	for _, tc := range []struct {
		name, schedule string
		usage          counts
		want           string // in currency units
	}{
		{"Haiku at its threshold", haiku, counts{cacheRead: 99_999, in: 1}, "0.00100009"},
		{"Haiku one token over", haiku, counts{cacheRead: 99_999, in: 2}, "0.00500095"},
		{"Haiku counts 1-hour writes toward the threshold", haiku, counts{cacheWrite1h: 100_001}, "0.100001"},
		{"Haiku leaves output out of the threshold", haiku, counts{in: 1, out: 200_000}, "0.1000001"},
		{"Haiku web search at the base price", haiku, counts{in: 10, webSearch: 3}, "0.030001"},
		{"a tier keeps the base prices it does not list", haiku, counts{cacheRead: 99_999, in: 2, webSearch: 1}, "0.01500095"},
		{"Gemini at its threshold", gemini, counts{cacheRead: 200_000}, "0.025"},
		{"Gemini one new token over", gemini, counts{cacheRead: 200_000, in: 1}, "0.0500025"},
		{"Gemini thinking at the tier's output price", gemini, counts{cacheRead: 200_000, in: 1, reasoning: 1000}, "0.0650025"},
		{"Gemini reply that only reasoned", gemini, counts{reasoning: 1000}, "0.01"},
		{"OpenAI cache-write sample", openAI, counts{cacheWrite: 12_000}, "0.15"},
		{"the highest tier exceeded", steps, counts{in: 25}, "75"},
		{"a lower tier", steps, counts{in: 15}, "30"},
		{"no usage", haiku, counts{}, "0"},
		{"the smallest price", smallest, counts{in: 1}, "0.000000000000001"},
		{"a zero price is a price", free, counts{cacheRead: 5}, "0"},
		{"a tier's zero price replaces the base price", free, counts{in: 11, out: 3}, "11"},
		{"Gemini text and images within the threshold", geminiImages, counts{in: 180_000, imageIn: 20_000}, "0.265"},
		{"Gemini images take the prompt over it", geminiImages, counts{in: 190_000, imageIn: 20_000}, "0.555"},
		{"audio at its own prices", audio, counts{in: 1, audioIn: 2, out: 3, audioOut: 4}, "107"},
		{"the largest cost", all, counts{
			in: 1e10, cacheRead: 1e10, cacheWrite: 1e10, cacheWrite1h: 1e10, audioIn: 1e10,
			imageIn: 1e10, out: 1e10, reasoning: 1e10, audioOut: 1e10, imageOut: 1e10, webSearch: 1e10, codeExec: 1e10,
		}, "120000000000000"},
	} {
		got, missing, err := mustParse(t, tc.schedule).Cost(mustUsage(t, tc.usage))
		want, _ := new(big.Rat).SetString(tc.want)
		if err != nil || got.Rat().Cmp(want) != 0 || missing != 0 {
			t.Errorf("%s: Cost = %s, missing %v, %v; want %s", tc.name, got.Rat().FloatString(15), members(missing), err, tc.want)
		}
	}
}

// reasoning falls back to output; nothing else falls back. Cost prices what it can and reports the rest, so billing
// can price the missing items at its fallback prices instead of losing the priced part (README.md「上架门槛与缺项」). In
// particular a cache write OpenAI reports against a schedule without a write price is reported, not priced as input.
func TestCostMissing(t *testing.T) {
	inputOnly := `{"v":1,"currency":"USD","rates":{"input":"1"}}`
	outputOnly := `{"v":1,"currency":"USD","rates":{"output":"1"}}`
	// Gemini 2.5 Pro without its cache prices: the unpriced cache reads still count toward the threshold.
	geminiNoCache := `{"v":1,"currency":"USD","rates":{"input":"0.00000125","output":"0.00001"},
		"context_tiers":{"basis":["input","cache_read"],"method":"whole",
			"tiers":[{"above":200000,"rates":{"input":"0.0000025","output":"0.000015"}}]}}`
	for _, tc := range []struct {
		schedule string
		usage    counts
		want     string
		missing  []catalog.Item
	}{
		{outputOnly, counts{out: 5, in: 1}, "5", []catalog.Item{in}},
		{inputOnly, counts{in: 5, cacheRead: 1}, "5", []catalog.Item{cacheRead}},
		{inputOnly, counts{in: 5, cacheWrite: 1}, "5", []catalog.Item{cacheWrite}},
		{inputOnly, counts{in: 5, cacheWrite1h: 1}, "5", []catalog.Item{cacheWrite1h}},
		{inputOnly, counts{in: 5, audioIn: 1}, "5", []catalog.Item{audioIn}},
		{inputOnly, counts{in: 5, imageIn: 1}, "5", []catalog.Item{imageIn}},
		{inputOnly, counts{in: 5, out: 1}, "5", []catalog.Item{out}},
		{inputOnly, counts{in: 5, reasoning: 1}, "5", []catalog.Item{reasoning}},
		{inputOnly, counts{in: 5, audioOut: 1}, "5", []catalog.Item{audioOut}},
		{inputOnly, counts{in: 5, imageOut: 1}, "5", []catalog.Item{imageOut}},
		{inputOnly, counts{in: 5, webSearch: 1}, "5", []catalog.Item{webSearch}},
		{inputOnly, counts{in: 5, codeExec: 1}, "5", []catalog.Item{codeExec}},
		{outputOnly, counts{out: 5, audioOut: 1}, "5", []catalog.Item{audioOut}}, // audio output is not output
		{inputOnly, counts{in: 5, out: 1, webSearch: 2, cacheRead: 0}, "5", []catalog.Item{out, webSearch}},
		{geminiNoCache, counts{cacheRead: 200_000, in: 1, out: 1000}, "0.0150025", []catalog.Item{cacheRead}},
	} {
		got, missing, err := mustParse(t, tc.schedule).Cost(mustUsage(t, tc.usage))
		want, _ := new(big.Rat).SetString(tc.want)
		if err != nil || got.Rat().Cmp(want) != 0 || !slices.Equal(members(missing), tc.missing) {
			t.Errorf("Cost of %v under %s = %s, missing %v, %v; want %s, missing %v",
				tc.usage, tc.schedule, got.Rat().FloatString(15), members(missing), err, tc.want, tc.missing)
		}
	}
}

// A service tier is priced with its own rates and context tiers, chosen in that order, and inherits nothing from the
// standard tier: what it does not price is missing, and a service tier the schedule does not price has every item
// missing (README.md「条件」).
func TestCostServiceTiers(t *testing.T) {
	s := mustParse(t, serviceTiers)
	standard, flex, priority, fast := catalog.ServiceTierStandard, catalog.ServiceTierFlex, catalog.ServiceTierPriority,
		catalog.ServiceTierFast
	for _, tc := range []struct {
		name    string
		tier    catalog.ServiceTier
		usage   counts
		want    string
		missing []catalog.Item
	}{
		{"standard", standard, counts{in: 1, out: 1}, "3", nil},
		{"standard above the threshold", standard, counts{in: 11, out: 1}, "15", nil},
		{"priority", priority, counts{in: 1, out: 1}, "4", nil},
		{"priority above the threshold", priority, counts{in: 11, out: 1}, "16", nil},
		{"fast above an unchanged threshold", fast, counts{in: 11, out: 1}, "78", nil},
		{"priority reasoning at priority's output price", priority, counts{in: 11, reasoning: 1}, "16", nil},
		{"standard cache reads", standard, counts{cacheRead: 1, in: 1, out: 1}, "4", nil},
		{"priority cache reads are not standard's", priority, counts{cacheRead: 1, in: 1, out: 1}, "4", []catalog.Item{cacheRead}},
		{"unpriced cache reads count toward the threshold", priority, counts{cacheRead: 10, in: 1, out: 1}, "6", []catalog.Item{cacheRead}},
		{"flex is not priced", flex, counts{in: 1, out: 1}, "0", []catalog.Item{in, out}},
	} {
		got, missing, err := s.Cost(mustTierUsage(t, tc.tier, tc.usage))
		want, _ := new(big.Rat).SetString(tc.want)
		if err != nil || got.Rat().Cmp(want) != 0 || !slices.Equal(members(missing), tc.missing) {
			t.Errorf("%s: Cost = %s, missing %v, %v; want %s, missing %v",
				tc.name, got.Rat().FloatString(15), members(missing), err, tc.want, tc.missing)
		}
	}
}

func members(s catalog.ItemSet) []catalog.Item {
	var items []catalog.Item
	for it := range catalog.NumItems {
		if s.Has(it) {
			items = append(items, it)
		}
	}
	return items
}

// Cost runs for every attempt of every request, so it must not allocate; the benchmark fails if it does.
func BenchmarkCost(b *testing.B) {
	s := mustParse(b, haiku)
	u := mustUsage(b, counts{in: 2_000, cacheRead: 99_000, cacheWrite: 500, out: 800, webSearch: 1})
	cost := func() {
		if _, missing, err := s.Cost(u); err != nil || missing != 0 {
			b.Fatal(err)
		}
	}
	if allocs := testing.AllocsPerRun(100, cost); allocs != 0 {
		b.Fatalf("Cost allocates %v times per call, want 0", allocs)
	}
	b.ReportAllocs()
	for b.Loop() {
		cost()
	}
}
