package catalog_test

import (
	"strings"
	"testing"

	"axiom/internal/catalog"
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
)

func mustParse(tb testing.TB, s string) catalog.Schedule {
	tb.Helper()
	schedule, err := catalog.ParseSchedule([]byte(s))
	if err != nil {
		tb.Fatal(err)
	}
	return schedule
}

// Equal schedules must be equal bytes: stored schedules are compared and deduplicated byte for byte.
func TestScheduleCanonicalJSON(t *testing.T) {
	// 2⁵³ + 1 has no float64, so a threshold that passed through one would come back as 2⁵³.
	bigThreshold := `{"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":10,"rates":{"input":"2"}},` +
		`{"above":9007199254740993,"rates":{"input":"3"}}]},"currency":"USD",` +
		`"rates":{"input":"1"},"v":1}`
	for text, want := range map[string]string{messy: haiku, haiku: haiku, bigThreshold: bigThreshold} {
		got, err := mustParse(t, text).MarshalJSON()
		if err != nil || string(got) != want {
			t.Errorf("MarshalJSON of %s =\n%s, %v; want\n%s", text, got, err, want)
		}
	}
}

// FuzzParseSchedule checks the boundary for entered prices: whatever parses has a canonical form that parses back to the
// same bytes, and prices bounded usage without an error.
func FuzzParseSchedule(f *testing.F) {
	for _, s := range []string{haiku, messy, gemini} {
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
		`"tiers":[{"above":10,"rates":{"input":"0.000002"}},{"above":20,"rates":{"input":"0.000003"}}]}}`
	mustParse(t, valid)
	mustParse(t, strings.Replace(valid, `"above":10`, `"above":0`, 1)) // a written 0 is a threshold
	// Every input-side item may count toward the threshold: Gemini's prompt includes audio, images and video.
	mustParse(t, strings.Replace(valid, `"basis":["input"]`,
		`"basis":["input","cache_read","cache_write","cache_write_1h","audio_in","image_in"]`, 1))
	for name, edit := range map[string][2]string{
		"excess method":       {`"method":"whole"`, `"method":"excess"`},
		"no method":           {`"method":"whole",`, ``},
		"unknown condition":   {`"v":1`, `"v":1,"time_of_day":{"from":"00:30","to":"08:30","rates":{"input":"0.5"}}`},
		"unknown tier member": {`"above":10,`, `"above":10,"per":"excess",`},
		"no basis":            {`"basis":["input"]`, `"basis":[]`},
		"output in basis":     {`"basis":["input"]`, `"basis":["input","output"]`},
		"web search in basis": {`"basis":["input"]`, `"basis":["input","web_search"]`},
		"audio out in basis":  {`"basis":["input"]`, `"basis":["input","audio_out"]`},
		"image out in basis":  {`"basis":["input"]`, `"basis":["input","image_out"]`},
		"no tiers":            {`"tiers":[{"above":10,"rates":{"input":"0.000002"}},{"above":20,"rates":{"input":"0.000003"}}]`, `"tiers":[]`},
		"no threshold":        {`"above":10,`, ``},
		"null threshold":      {`"above":10`, `"above":null`},
		"no tier rates":       {`,"rates":{"input":"0.000002"}`, ``},
		"empty tier rates":    {`"rates":{"input":"0.000002"}`, `"rates":{}`},
		"equal thresholds":    {`"above":20`, `"above":10`},
		"falling thresholds":  {`"above":20`, `"above":5`},
		"negative threshold":  {`"above":10`, `"above":-1`},
		"unknown basis item":  {`"basis":["input"]`, `"basis":["input","prompt"]`},
		"basis item twice":    {`"basis":["input"]`, `"basis":["input","input"]`},
		"unknown rate item":   {`"input":"0.000001"`, `"audio_input":"0.000001"`},
		"exponent price":      {`"input":"0.000001"`, `"input":"1e-6"`},
		"number price":        {`"input":"0.000001"`, `"input":0.000001`},
		"price twice":         {`"input":"0.000001"`, `"input":"0.000001","input":"0.000002"`},
		"version 2":           {`"v":1`, `"v":2`},
		"no version":          {`"v":1,`, ``},
		"no currency":         {`"currency":"USD",`, ``},
		"lower-case currency": {`"USD"`, `"usd"`},
		"input modalities":    {`"v":1`, `"v":1,"input_modalities":["text"]`},
	} {
		text := strings.Replace(valid, edit[0], edit[1], 1)
		if text == valid {
			t.Fatalf("%s: %q is not in the valid schedule", name, edit[0])
		}
		if _, err := catalog.ParseSchedule([]byte(text)); err == nil {
			t.Errorf("%s: ParseSchedule(%s) succeeded, want an error", name, text)
		}
	}
}
