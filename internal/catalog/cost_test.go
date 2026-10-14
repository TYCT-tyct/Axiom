package catalog_test

import (
	"errors"
	"math/big"
	"testing"

	"axiom/internal/catalog"
)

func mustUsage(tb testing.TB, counts map[catalog.Item]int64) catalog.Usage {
	tb.Helper()
	u, err := catalog.NewUsage(counts)
	if err != nil {
		tb.Fatal(err)
	}
	return u
}

type counts = map[catalog.Item]int64

const (
	in, cacheRead, cacheWrite, cacheWrite1h = catalog.ItemInput, catalog.ItemCacheRead, catalog.ItemCacheWrite, catalog.ItemCacheWrite1h
	out, reasoning, webSearch               = catalog.ItemOutput, catalog.ItemReasoning, catalog.ItemWebSearch
)

func TestCost(t *testing.T) {
	// OpenAI's prompt caching sample: 12,000 input tokens, all of them written to the cache, map to cache_write alone
	// (README.md「计费项与用量」); the write price replaces the input price and the write counts toward the threshold.
	// The prices and the threshold are made up to put the sample right above it.
	openAI := `{"v":1,"currency":"USD","input_modalities":["text"],
		"rates":{"input":"0.000005","cache_read":"0.0000005","cache_write":"0.00000625","output":"0.00003"},
		"context_tiers":{"basis":["input","cache_read","cache_write"],"method":"whole",
			"tiers":[{"above":11999,"rates":{"cache_write":"0.0000125"}}]}}`
	steps := `{"v":1,"currency":"CNY","input_modalities":["text"],"rates":{"input":"1"},
		"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":10,"rates":{"input":"2"}},{"above":20,"rates":{"input":"3"}}]}}`
	all := `{"v":1,"currency":"USD","input_modalities":["text"],"rates":{"input":"1000","cache_read":"1000","cache_write":"1000",
		"cache_write_1h":"1000","output":"1000","reasoning":"1000","web_search":"1000"}}`
	smallest := `{"v":1,"currency":"USD","input_modalities":["text"],"rates":{"input":"0.000000000000001"}}`
	free := `{"v":1,"currency":"USD","input_modalities":["text"],"rates":{"input":"1","cache_read":"0","output":"1"},
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
		{"the largest cost", all, counts{in: 1e10, cacheRead: 1e10, cacheWrite: 1e10, cacheWrite1h: 1e10, out: 1e10, reasoning: 1e10, webSearch: 1e10}, "70000000000000"},
	} {
		got, err := mustParse(t, tc.schedule).Cost(mustUsage(t, tc.usage))
		want, _ := new(big.Rat).SetString(tc.want)
		if err != nil || got.Rat().Cmp(want) != 0 {
			t.Errorf("%s: Cost = %s, %v; want %s", tc.name, got.Rat().FloatString(15), err, tc.want)
		}
	}
}

// reasoning falls back to output; nothing else falls back. In particular a cache write OpenAI reports against a
// schedule without a write price is an error, not an input token.
func TestCostUnpriced(t *testing.T) {
	inputOnly := mustParse(t, `{"v":1,"currency":"USD","input_modalities":["text"],"rates":{"input":"0.000001"}}`)
	outputOnly := mustParse(t, `{"v":1,"currency":"USD","input_modalities":["text"],"rates":{"output":"0.000001"}}`)
	for _, tc := range []struct {
		schedule catalog.Schedule
		usage    counts
	}{
		{outputOnly, counts{out: 5, in: 1}},
		{inputOnly, counts{in: 5, cacheRead: 1}},
		{inputOnly, counts{in: 5, cacheWrite: 1}},
		{inputOnly, counts{in: 5, cacheWrite1h: 1}},
		{inputOnly, counts{in: 5, out: 1}},
		{inputOnly, counts{in: 5, reasoning: 1}},
		{inputOnly, counts{in: 5, webSearch: 1}},
	} {
		if got, err := tc.schedule.Cost(mustUsage(t, tc.usage)); !errors.Is(err, catalog.ErrUnpriced) {
			t.Errorf("Cost of %v = %v, %v; want ErrUnpriced", tc.usage, got.Rat(), err)
		}
	}
}

// Cost runs for every attempt of every request, so it must not allocate; the benchmark fails if it does.
func BenchmarkCost(b *testing.B) {
	s := mustParse(b, haiku)
	u := mustUsage(b, counts{in: 2_000, cacheRead: 99_000, cacheWrite: 500, out: 800, webSearch: 1})
	cost := func() {
		if _, err := s.Cost(u); err != nil {
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
