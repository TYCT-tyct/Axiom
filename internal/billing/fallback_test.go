package billing

import (
	"errors"
	"math"
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform"
)

// testFallbacks prices a web search at 20 billionths, a code execution at 50 and corrects by cost at 1.5 times.
const testFallbacks = `{"currency":"USD","markup":"1.5","per_use":{"code_execution":"0.00000005","web_search":"0.00000002"}}`

func mustFallbacks(tb testing.TB, body string) fallbacks {
	tb.Helper()
	f, err := parseFallbacks(body)
	if err != nil {
		tb.Fatal(err)
	}
	return f
}

// TestParseFallbacks: a row is valid only with a currency, a price above 0 and at most 1 for each known per-use item
// and nothing else, and a markup from 1 to 10 (docs/domains/billing.md「价目表缺项」: 币种、单位正确，不为 0).
func TestParseFallbacks(t *testing.T) {
	row := func(currency, markup, search, code string) string {
		return `{"currency":"` + currency + `","markup":"` + markup + `","per_use":{"code_execution":"` + code + `","web_search":"` + search + `"}}`
	}
	for _, tc := range []struct {
		name, body string
		ok         bool
	}{
		{"valid", testFallbacks, true},
		{"the seeded row", `{"currency":"USD","markup":"1.2","per_use":{"code_execution":"0.05","web_search":"0.025"}}`, true},
		{"markup at the bounds", row("CNY", "1", "1", "0.000000000000001"), true},
		{"markup at the top", row("USD", "10", "0.01", "0.01"), true},
		{"markup below 1 sells below cost", row("USD", "0.99", "0.01", "0.01"), false},
		{"markup above 10", row("USD", "10.000000000000001", "0.01", "0.01"), false},
		{"a free per-use item", row("USD", "1.2", "0", "0.01"), false},
		{"a per-use price above 1", row("USD", "1.2", "1.01", "0.01"), false},
		{"a price that is no decimal", row("USD", "1.2", "1e-2", "0.01"), false},
		{"a currency in lower case", row("usd", "1.2", "0.01", "0.01"), false},
		{"an item missing", `{"currency":"USD","markup":"1.2","per_use":{"web_search":"0.01"}}`, false},
		{"an item that is not per use", `{"currency":"USD","markup":"1.2","per_use":{"code_execution":"0.01","input":"0.01","web_search":"0.01"}}`, false},
		{"an unknown member", `{"currency":"USD","markup":"1.2","per_use":{"code_execution":"0.01","web_search":"0.01"},"v":1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseFallbacks(tc.body)
			if (err == nil) != tc.ok || err != nil && !errors.Is(err, errFallbacks) {
				t.Fatalf("parseFallbacks = %v, want ok %v", err, tc.ok)
			}
			if err != nil {
				return
			}
			if body, err := f.body(); err != nil || body != tc.body { // every valid fixture is written canonically
				t.Errorf("body = %s, %v; want %s", body, err, tc.body)
			}
		})
	}
}

// TestUses: an unknown value counts its uses, rounded up and bounded; anything else is one use.
func TestUses(t *testing.T) {
	for v, want := range map[string]int64{
		"2": 2, "0": 0, "2.5": 3, "2.000": 2, "0.1": 1, `"scale"`: 1, "1e3": 1, "-1": 1, "": 1, ".5": 1, "3.": 1,
		"9999999999": 9999999999, "9999999999.5": 1e10, "10000000000": 1e10, strings.Repeat("9", 40): 1e10,
	} {
		if got := uses(v); got != want {
			t.Errorf("uses(%q) = %d, want %d", v, got, want)
		}
	}
}

// TestPriceSegmentsFallbacks pins every fallback price at input and output 1 a token, 50 input and 20 output (70):
// unknown tokens at the highest token price, unknown uses at the dearest per-use default, both estimated; a cached
// modality intersection is recorded and free; a waiver keeps the uses and drops the tokens; a free model's unknown tokens are free with it, but its uses are not.
func TestPriceSegmentsFallbacks(t *testing.T) {
	s := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.000000001","output":"0.000000001"},
		"service_tiers":{"priority":{"rates":{"input":"0.000000003","output":"0.000000002"}}}}`)
	free := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0","output":"0"}}`)
	f := mustFallbacks(t, testFallbacks)
	for _, tc := range []struct {
		name         string
		s            catalog.Schedule
		field, value string
		waived       bool
		want         int64
	}{
		{"tokens at the highest token price, any tier", s, "usage.new_thing_tokens", "30", false, 70 + 90},
		{"uses at the dearest default", s, "usage.server_tool_use.web_fetch_requests", "2", false, 70 + 100},
		{"a fraction of a use rounds up", s, "usage.server_tool_use.web_fetch_requests", "1.2", false, 70 + 100},
		{"a service tier is one use", s, "usage.service_tier", `"scale"`, false, 70 + 50},
		{"tokens past the bound are uses", s, "usage.new_thing_tokens", "20000000000", false, 70 + 50*1e10},
		{"a cached intersection member costs nothing", s, "usage.input_tokens_details.cached_tokens_details.video_tokens", "800", false, 70},
		{"a cached intersection member in Chat", s, "usage.prompt_tokens_details.cached_tokens_details.video_tokens", "800", true, 0},
		{"waived: the tokens go", s, "usage.new_thing_tokens", "30", true, 0},
		{"waived: the uses stay", s, "usage.server_tool_use.web_fetch_requests", "2", true, 100},
		{"a free model's tokens", free, "usage.new_thing_tokens", "30", false, 0},
		{"a free model's uses", free, "usage.server_tool_use.web_fetch_requests", "2", false, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := priceSegments(tc.s, f, []Segment{unknown(t, 50, tc.field, tc.value)}, tc.waived, 0)
			if err != nil || p.price != tc.want || !p.estimated {
				t.Errorf("priceSegments = %+v, %v; want %d estimated", p, err, tc.want)
			}
		})
	}
}

// TestPriceSegmentsPricesATierTheListLacks: a schedule that prices the standard tier only still charges a call its
// upstream served at priority, which it prices nowhere, at the fallback prices: tokens at the highest token price
// anywhere in the schedule and the search at its site's default, marked estimated; a waiver keeps the search alone; and
// each sampling is charged, not only the last, which search is what makes several of
// (docs/domains/billing.md「定价」: 上游报告了列表之外的等级，那次调用的各项按兜底价收并标暂估).
func TestPriceSegmentsPricesATierTheListLacks(t *testing.T) {
	f := mustFallbacks(t, testFallbacks)
	s := mustSchedule(t, `{"v":1,"currency":"USD",
		"rates":{"input":"0.000000001","output":"0.000000001","web_search":"0.00000001"}}`).Fill(f.perUse)
	usage := func(counts map[catalog.Item]int64) Segment {
		t.Helper()
		u, err := catalog.NewUsage(counts, catalog.ServiceTierPriority, nil)
		if err != nil {
			t.Fatal(err)
		}
		return Segment{Usage: u, Reported: ^catalog.ItemSet(0)}
	}
	tokens := usage(map[catalog.Item]int64{in: 50, catalog.ItemOutput: 20, webSearch: 1})
	for _, tc := range []struct {
		name     string
		waived   bool
		segments []Segment
		want     int64
	}{
		{"tokens at the highest token price, the search at its default", false, []Segment{tokens}, 70 + 20},
		{"waived: the tokens go, the search stays", true, []Segment{tokens}, 20},
		{"two samplings, each charged", false, []Segment{usage(map[catalog.Item]int64{in: 50}), tokens}, 50 + 90},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := priceSegments(s, f, tc.segments, tc.waived, 0)
			if err != nil || p.price != tc.want || !p.estimated {
				t.Errorf("priceSegments = %+v, %v; want %d estimated", p, err, tc.want)
			}
		})
	}
}

// TestPriceSegmentsSaturates: the dearest valid defaults (one credit a use) at the most uses an unknown value counts,
// alone, twice in a sampling or over two samplings, price past what an amount holds: the charge is the most an amount
// holds, not a panic.
func TestPriceSegmentsSaturates(t *testing.T) {
	s := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.000000001","output":"0.000000001"}}`)
	f := mustFallbacks(t, `{"currency":"USD","markup":"10","per_use":{"code_execution":"1","web_search":"1"}}`)
	most := func(field string) catalog.UnknownUsage {
		return catalog.UnknownUsage{Field: field, Value: "10000000000"}
	}
	for name, segments := range map[string][]Segment{
		"one field":     {segment(t, nil, most("a"))},
		"two fields":    {segment(t, nil, most("a"), most("b"))},
		"two samplings": {segment(t, nil, most("a")), segment(t, nil, most("a"))},
		"just inside":   {segment(t, nil, catalog.UnknownUsage{Field: "a", Value: "9223372035"})},
	} {
		want := int64(math.MaxInt64)
		if name == "just inside" {
			want = 9223372035e9
		}
		if p, err := priceSegments(s, f, segments, false, 0); err != nil || p.price != want || !p.estimated {
			t.Errorf("%s: priceSegments = %+v, %v; want %d estimated", name, p, err, want)
		}
	}
}

// TestFillPricesPerUse: a schedule filled with the defaults prices a known per-use item it lacks exactly, not as an
// estimate, and keeps its own price where it has one.
func TestFillPricesPerUse(t *testing.T) {
	f := mustFallbacks(t, testFallbacks)
	s := mustSchedule(t, `{"v":1,"currency":"USD","rates":{"input":"0.000000001","output":"0.000000001","web_search":"0.00000001"}}`).Fill(f.perUse)
	u, err := catalog.NewUsage(map[catalog.Item]int64{in: 50, catalog.ItemOutput: 20, webSearch: 2, catalog.ItemCodeExecution: 3},
		catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := priceSegments(s, f, []Segment{{Usage: u, Reported: ^catalog.ItemSet(0)}}, false, 0)
	if want := int64(70 + 2*10 + 3*50); err != nil || p.price != want || p.estimated {
		t.Errorf("priceSegments = %+v, %v; want %d, not estimated", p, err, want)
	}
	if got := f.highest(); got.Cmp(mustUnitPrice("0.00000005")) != 0 {
		t.Errorf("highest = %s", got)
	}
}

// exact is a decimal from a test table; the empty amount is zero.
func exact(t *testing.T, s string) platform.Exact {
	t.Helper()
	if s == "" {
		return platform.Exact{}
	}
	e, err := platform.ParseExact(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return e
}

// TestPriceSegmentsUsageExtras: the two amounts a client's usage shows beside the token counts are sale amounts at the
// same rates as the charge: every per-use item the call ran, and what cache reads saved against the input price. A
// cache read priced at or above input saves nothing, and no per-use item means no server tool cost.
func TestPriceSegmentsUsageExtras(t *testing.T) {
	f := mustFallbacks(t, testFallbacks)
	for _, tc := range []struct {
		name       string
		schedule   string
		counts     map[catalog.Item]int64
		wantTool   string
		wantSaving string
		tool       bool
	}{
		{
			name: "a search and a code execution, cache below input",
			schedule: `{"v":1,"currency":"USD","rates":{"input":"0.000000001","cache_read":"0.0000000003",
				"output":"0.000000002","web_search":"0.02","code_execution":"0.03"}}`,
			counts: map[catalog.Item]int64{
				in: 10, catalog.ItemCacheRead: 100, catalog.ItemOutput: 1,
				webSearch: 2, catalog.ItemCodeExecution: 1,
			},
			wantTool: "0.07", wantSaving: "0.00000007", tool: true,
		},
		{
			name:     "cache at input price saves nothing",
			schedule: `{"v":1,"currency":"USD","rates":{"input":"0.000000001","cache_read":"0.000000001","output":"0.000000002"}}`,
			counts:   map[catalog.Item]int64{in: 10, catalog.ItemCacheRead: 100, catalog.ItemOutput: 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := catalog.NewUsage(tc.counts, catalog.ServiceTierStandard, nil)
			if err != nil {
				t.Fatal(err)
			}
			p, err := priceSegments(mustSchedule(t, tc.schedule), f, []Segment{{Usage: u, Reported: ^catalog.ItemSet(0)}}, false, 0)
			if err != nil {
				t.Fatal(err)
			}
			wantTool, wantSaving := exact(t, tc.wantTool), exact(t, tc.wantSaving)
			if p.hasServerTool != tc.tool || p.serverTool != wantTool || p.cacheDiscount != wantSaving {
				t.Errorf("extras = tool %v %v (has %v), saving %v; want %v (has %v), %v",
					p.serverTool, p.hasServerTool, tc.tool, p.cacheDiscount, wantTool, tc.tool, wantSaving)
			}
		})
	}
}
