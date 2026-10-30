package catalog_test

import (
	"slices"
	"strings"
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/platform"
)

// Official prices per token from the vendors' pricing pages, 2026-10-14: Claude Haiku 5.5 ($0.10 / $0.50 per million
// input / output up to 100,000 prompt tokens, higher above; web search $10 per 1,000) and Gemini 2.5 Pro ($1.25 /
// $10 up to 200k, $2.50 / $15 above; output includes thinking, so reasoning has no price of its own). The Haiku
// schedule is written in canonical form, and messy is the same schedule in another order and spelling.
const (
	haiku = `{"context_tiers":{"basis":["input","cache_read","cache_write","cache_write_1h"],"method":"whole",` +
		`"tiers":[{"above":100000,"rates":{"cache_read":"0.00000005","cache_write":"0.000000625",` +
		`"cache_write_1h":"0.000001","input":"0.0000005","output":"0.0000025"}}]},"currency":"USD",` +
		`"rates":{"cache_read":"0.00000001","cache_write":"0.000000125",` +
		`"cache_write_1h":"0.0000002","input":"0.0000001","output":"0.0000005","web_search":"0.01"},"v":1}`
	messy = ` { "v": 1, "rates": { "web_search": "0.010", "output": "0.0000005", "input": "0.00000010",
		"cache_write_1h": "0.0000002", "cache_write": "0.000000125", "cache_read": "0.000000010" },
		"currency": "USD",
		"context_tiers": { "tiers": [ { "rates": { "output": "0.00000250", "input": "0.0000005",
			"cache_write_1h": "0.000001", "cache_write": "0.000000625", "cache_read": "0.00000005" },
			"above": 100000 } ], "method": "whole", "basis": ["cache_write_1h", "cache_write", "cache_read", "input"] } }`
	gemini = `{"v":1,"currency":"USD",
		"rates":{"input":"0.00000125","cache_read":"0.000000125","output":"0.00001"},
		"context_tiers":{"basis":["input","cache_read"],"method":"whole",
			"tiers":[{"above":200000,"rates":{"input":"0.0000025","cache_read":"0.00000025","output":"0.000015"}}]}}`
	// README.md「条件」's example, in canonical form: standard output 2, 4 above the threshold; priority output 3, 5
	// above it, and no cache_read price; fast mode at its own prices, with no change at the threshold; no flex prices.
	// The prices and the threshold are made up.
	serviceTiers = `{"context_tiers":{"basis":["input","cache_read"],"method":"whole","tiers":[{"above":10,` +
		`"rates":{"output":"4"}}]},"currency":"USD","rates":{"cache_read":"1","input":"1","output":"2"},` +
		`"service_tiers":{"fast":{"context_tiers":[{"above":10,"rates":{}}],"rates":{"input":"6","output":"12"}},` +
		`"priority":{"context_tiers":[{"above":10,"rates":{"output":"5"}}],"rates":{"input":"1","output":"3"}}},"v":1}`
)

func mustParse(tb testing.TB, s string) catalog.Schedule {
	tb.Helper()
	schedule, err := catalog.ParseSchedule([]byte(s))
	if err != nil {
		tb.Fatal(err)
	}
	return schedule
}

func mustCurrency(tb testing.TB, code string) platform.Currency {
	tb.Helper()
	c, err := platform.ParseCurrency(code)
	if err != nil {
		tb.Fatal(err)
	}
	return c
}

// Equal schedules must be equal bytes: stored schedules are compared and deduplicated byte for byte.
func TestScheduleCanonicalJSON(t *testing.T) {
	// 2⁵³ + 1 has no float64, so a threshold that passed through one would come back as 2⁵³.
	bigThreshold := `{"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":10,"rates":{"input":"2"}},` +
		`{"above":9007199254740993,"rates":{"input":"3"}}]},"currency":"USD",` +
		`"rates":{"input":"1"},"v":1}`
	for text, want := range map[string]string{
		messy: haiku, haiku: haiku, bigThreshold: bigThreshold, serviceTiers: serviceTiers,
	} {
		got, err := mustParse(t, text).MarshalJSON()
		if err != nil || string(got) != want {
			t.Errorf("MarshalJSON of %s =\n%s, %v; want\n%s", text, got, err, want)
		}
	}
}

// FuzzParseSchedule checks the boundary for entered prices: whatever parses has a canonical form that parses back to the
// same bytes, and prices bounded usage without an error.
func FuzzParseSchedule(f *testing.F) {
	for _, s := range []string{haiku, messy, gemini, serviceTiers} {
		f.Add(s)
	}
	u := mustUsage(f, counts{in: 1e10, cacheRead: 7, cacheWrite: 1, out: 1e10, reasoning: 3, webSearch: 1})
	f.Fuzz(func(t *testing.T, text string) {
		s, err := catalog.ParseSchedule([]byte(text))
		if err != nil {
			return
		}
		canonical, err := s.MarshalJSON()
		if err != nil {
			t.Fatalf("MarshalJSON of %s: %v", text, err)
		}
		if again, err := mustParse(t, string(canonical)).MarshalJSON(); err != nil || string(again) != string(canonical) {
			t.Fatalf("%s writes %s, which writes %s, %v", text, canonical, again, err)
		}
		if _, _, err := s.Cost(u); err != nil {
			t.Fatalf("Cost under %s: %v", canonical, err)
		}
	})
}

// A schedule with a condition M1 cannot compute, or with a member left out, is refused when it is saved, not priced
// wrongly later.
func TestParseScheduleRejects(t *testing.T) {
	valid := `{"v":1,"currency":"USD","rates":{"input":"0.000001"},` +
		`"context_tiers":{"basis":["input"],"method":"whole",` +
		`"tiers":[{"above":10,"rates":{"input":"0.000002"}},{"above":20,"rates":{"input":"0.000003"}}]},` +
		`"service_tiers":{"priority":{"rates":{"input":"0.000004"},` +
		`"context_tiers":[{"above":10,"rates":{}},{"above":20,"rates":{"input":"0.000005"}}]}}}`
	mustParse(t, valid)
	mustParse(t, strings.ReplaceAll(valid, `"above":10`, `"above":0`)) // a written 0 is a threshold
	// A threshold where only another service tier's prices change: every service tier lists every threshold.
	mustParse(t, strings.Replace(valid, `"rates":{"input":"0.000002"}`, `"rates":{}`, 1))
	// Every input-side item may count toward the threshold: Gemini's prompt includes audio, images and video.
	mustParse(t, strings.Replace(valid, `"basis":["input"]`,
		`"basis":["input","cache_read","cache_write","cache_write_1h","audio_in","image_in"]`, 1))
	for name, edit := range map[string][2]string{
		"excess method":                        {`"method":"whole"`, `"method":"excess"`},
		"no method":                            {`"method":"whole",`, ``},
		"unknown condition":                    {`"v":1`, `"v":1,"time_of_day":{"from":"00:30","to":"08:30","rates":{"input":"0.5"}}`},
		"unknown tier member":                  {`"above":10,`, `"above":10,"per":"excess",`},
		"no basis":                             {`"basis":["input"]`, `"basis":[]`},
		"output in basis":                      {`"basis":["input"]`, `"basis":["input","output"]`},
		"web search in basis":                  {`"basis":["input"]`, `"basis":["input","web_search"]`},
		"code execution in basis":              {`"basis":["input"]`, `"basis":["input","code_execution"]`},
		"audio out in basis":                   {`"basis":["input"]`, `"basis":["input","audio_out"]`},
		"image out in basis":                   {`"basis":["input"]`, `"basis":["input","image_out"]`},
		"no tiers":                             {`"tiers":[{"above":10,"rates":{"input":"0.000002"}},{"above":20,"rates":{"input":"0.000003"}}]`, `"tiers":[]`},
		"no threshold":                         {`"above":10,`, ``},
		"null threshold":                       {`"above":10`, `"above":null`},
		"no tier rates":                        {`,"rates":{"input":"0.000002"}`, ``},
		"equal thresholds":                     {`"above":20`, `"above":10`},
		"falling thresholds":                   {`"above":20`, `"above":5`},
		"negative threshold":                   {`"above":10`, `"above":-1`},
		"unknown basis item":                   {`"basis":["input"]`, `"basis":["input","prompt"]`},
		"basis item twice":                     {`"basis":["input"]`, `"basis":["input","input"]`},
		"unknown rate item":                    {`"input":"0.000001"`, `"audio_input":"0.000001"`},
		"exponent price":                       {`"input":"0.000001"`, `"input":"1e-6"`},
		"number price":                         {`"input":"0.000001"`, `"input":0.000001`},
		"price twice":                          {`"input":"0.000001"`, `"input":"0.000001","input":"0.000002"`},
		"version 2":                            {`"v":1`, `"v":2`},
		"no version":                           {`"v":1,`, ``},
		"no currency":                          {`"currency":"USD",`, ``},
		"lower-case currency":                  {`"USD"`, `"usd"`},
		"input modalities":                     {`"v":1`, `"v":1,"input_modalities":["text"]`},
		"no rates":                             {`"rates":{"input":"0.000001"},`, ``},
		"standard as a service tier":           {`"priority":`, `"standard":`},
		"unknown service tier":                 {`"priority":`, `"scale":`},
		"service tier without rates":           {`"rates":{"input":"0.000004"},`, ``},
		"service tier tier without rates":      {`{"above":10,"rates":{}}`, `{"above":10}`},
		"service tier missing a threshold":     {`{"above":10,"rates":{}},`, ``},
		"service tier with another threshold":  {`{"above":20,"rates":{"input":"0.000005"}}`, `{"above":21,"rates":{"input":"0.000005"}}`},
		"service tier with an extra threshold": {`{"above":20,"rates":{"input":"0.000005"}}`, `{"above":20,"rates":{}},{"above":30,"rates":{}}`},
		"service tier prices an unknown item":  {`"input":"0.000004"`, `"pdf":"0.000004"`},
		"service tier twice":                   {`"service_tiers":{`, `"service_tiers":{"priority":{"rates":{}},`},
	} {
		// Every occurrence, so that a threshold edit reaches the standard tier and the service tier alike and is refused by
		// its own check rather than by the thresholds differing.
		text := strings.ReplaceAll(valid, edit[0], edit[1])
		if text == valid {
			t.Fatalf("%s: %q is not in the valid schedule", name, edit[0])
		}
		if _, err := catalog.ParseSchedule([]byte(text)); err == nil {
			t.Errorf("%s: ParseSchedule(%s) succeeded, want an error", name, text)
		}
	}
}

// TestScheduleScaled: every price, in every tier and service tier, is multiplied; the currency becomes the one asked for;
// an item without a price stays without one; and a price that does not divide at the finest price a schedule holds is
// rounded up by less than one place.
func TestScheduleView(t *testing.T) {
	// Each item's effective price at each band, in service tier, band and item order. A band that leaves an item out
	// inherits the base price; an empty band therefore restores the base prices rather than removing them.
	got := mustParse(t, serviceTiers).View()
	if got.Currency != mustCurrency(t, "USD") {
		t.Errorf("currency: %v", got.Currency)
	}
	if want := []catalog.Item{catalog.ItemInput, catalog.ItemCacheRead}; !slices.Equal(got.Basis, want) {
		t.Errorf("basis: %v, want %v", got.Basis, want)
	}
	type rate struct {
		tier  catalog.ServiceTier
		base  bool
		above int64
		item  catalog.Item
		price string
	}
	want := []rate{
		{catalog.ServiceTierStandard, true, 0, catalog.ItemInput, "1"},
		{catalog.ServiceTierStandard, true, 0, catalog.ItemCacheRead, "1"},
		{catalog.ServiceTierStandard, true, 0, catalog.ItemOutput, "2"},
		{catalog.ServiceTierStandard, false, 10, catalog.ItemInput, "1"},
		{catalog.ServiceTierStandard, false, 10, catalog.ItemCacheRead, "1"},
		{catalog.ServiceTierStandard, false, 10, catalog.ItemOutput, "4"},
		{catalog.ServiceTierPriority, true, 0, catalog.ItemInput, "1"},
		{catalog.ServiceTierPriority, true, 0, catalog.ItemOutput, "3"},
		{catalog.ServiceTierPriority, false, 10, catalog.ItemInput, "1"},
		{catalog.ServiceTierPriority, false, 10, catalog.ItemOutput, "5"},
		{catalog.ServiceTierFast, true, 0, catalog.ItemInput, "6"},
		{catalog.ServiceTierFast, true, 0, catalog.ItemOutput, "12"},
		{catalog.ServiceTierFast, false, 10, catalog.ItemInput, "6"},
		{catalog.ServiceTierFast, false, 10, catalog.ItemOutput, "12"},
	}
	if len(got.Rates) != len(want) {
		t.Fatalf("rates: %d, want %d: %+v", len(got.Rates), len(want), got.Rates)
	}
	for i, w := range want {
		g := got.Rates[i]
		if g.ServiceTier != w.tier || g.Base != w.base || g.Above != w.above || g.Item != w.item || g.Price.String() != w.price {
			t.Errorf("rate %d: %+v, want %+v", i, g, w)
		}
	}
}

// TestScheduleViewKeepsBandsApart covers the two cases that a view of only the explicit prices gets wrong: a first
// threshold of 0 is a band of its own, not the base; and an empty band restores the base prices instead of being
// dropped, so "2 above 100" does not silently become "2 forever".
func TestScheduleViewKeepsBandsApart(t *testing.T) {
	for name, tc := range map[string]struct {
		schedule string
		want     []struct {
			base  bool
			above int64
			price string
		}
	}{
		"zero threshold": {
			`{"v":1,"currency":"USD","rates":{"input":"0.000001"},"context_tiers":{"basis":["input"],"method":"whole",` +
				`"tiers":[{"above":0,"rates":{"input":"0.000002"}}]}}`,
			[]struct {
				base  bool
				above int64
				price string
			}{{true, 0, "0.000001"}, {false, 0, "0.000002"}},
		},
		"empty band restores base": {
			`{"v":1,"currency":"USD","rates":{"input":"1"},"context_tiers":{"basis":["input"],"method":"whole",` +
				`"tiers":[{"above":100,"rates":{"input":"2"}},{"above":200,"rates":{}}]}}`,
			[]struct {
				base  bool
				above int64
				price string
			}{{true, 0, "1"}, {false, 100, "2"}, {false, 200, "1"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := mustParse(t, tc.schedule).View().Rates
			if len(got) != len(tc.want) {
				t.Fatalf("rates: %+v, want %d", got, len(tc.want))
			}
			for i, w := range tc.want {
				g := got[i]
				if g.Base != w.base || g.Above != w.above || g.Price.String() != w.price {
					t.Errorf("rate %d: %+v, want base %v above %d price %s", i, g, w.base, w.above, w.price)
				}
			}
		})
	}
}
func TestScheduleScaled(t *testing.T) {
	t.Parallel()
	cny, usd := mustCurrency(t, "CNY"), mustCurrency(t, "USD")
	scaled, err := mustParse(t, haiku).Scaled("1.2", cny)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"context_tiers":{"basis":["input","cache_read","cache_write","cache_write_1h"],"method":"whole",` +
		`"tiers":[{"above":100000,"rates":{"cache_read":"0.00000006","cache_write":"0.00000075",` +
		`"cache_write_1h":"0.0000012","input":"0.0000006","output":"0.000003"}}]},"currency":"CNY",` +
		`"rates":{"cache_read":"0.000000012","cache_write":"0.00000015",` +
		`"cache_write_1h":"0.00000024","input":"0.00000012","output":"0.0000006","web_search":"0.012"},"v":1}`
	got, err := scaled.MarshalJSON()
	if err != nil || string(got) != want {
		t.Errorf("Scaled =\n%s, %v; want\n%s", got, err, want)
	}
	// 1 × 1/3 has no finite decimal form: the finest place, up.
	tiny, err := mustParse(t, `{"v":1,"currency":"USD","rates":{"input":"1"}}`).Scaled("0.3333333333333333333", usd)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := tiny.MarshalJSON(); err != nil || !strings.Contains(string(b), `"input":"0.333333333333334"`) {
		t.Errorf("1 × 0.333… = %s, %v; want the price rounded up at 10⁻¹⁵", b, err)
	}
	if _, err := mustParse(t, haiku).Scaled("-1", usd); err == nil {
		t.Error("a negative multiplier was taken")
	}
	if _, err := mustParse(t, haiku).Scaled("1000000", usd); err == nil {
		t.Error("a price past 1000 was taken")
	}
}
