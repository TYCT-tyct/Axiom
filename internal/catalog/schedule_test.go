package catalog_test

import (
	"strings"
	"testing"

	"axiom/internal/catalog"
)

// Official prices per token from the vendors' pricing pages, 2026-10-14: Claude Haiku 5.5 ($0.10 / $0.50 per million
// input / output up to 100,000 prompt tokens, higher above; web search $10 per 1,000) and Gemini 2.5 Pro ($1.25 /
// $10 up to 200k, $2.50 / $15 above; output includes thinking, so reasoning has no price of its own). The Haiku
// schedule is written in canonical form.
const (
	haiku = `{"context_tiers":{"basis":["input","cache_read","cache_write","cache_write_1h"],"method":"whole",` +
		`"tiers":[{"above":100000,"rates":{"cache_read":"0.00000005","cache_write":"0.000000625",` +
		`"cache_write_1h":"0.000001","input":"0.0000005","output":"0.0000025"}}]},"currency":"USD",` +
		`"input_modalities":["text","image"],"rates":{"cache_read":"0.00000001","cache_write":"0.000000125",` +
		`"cache_write_1h":"0.0000002","input":"0.0000001","output":"0.0000005","web_search":"0.01"},"v":1}`
	gemini = `{"v":1,"currency":"USD","input_modalities":["text"],
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
	messy := ` { "v": 1, "rates": { "web_search": "0.010", "output": "0.0000005", "input": "0.00000010",
		"cache_write_1h": "0.0000002", "cache_write": "0.000000125", "cache_read": "0.000000010" },
		"input_modalities": ["image", "text"], "currency": "USD",
		"context_tiers": { "tiers": [ { "rates": { "output": "0.00000250", "input": "0.0000005",
			"cache_write_1h": "0.000001", "cache_write": "0.000000625", "cache_read": "0.00000005" },
			"above": 100000 } ], "method": "whole", "basis": ["cache_write_1h", "cache_write", "cache_read", "input"] } }`
	for _, in := range []string{messy, haiku} {
		got, err := mustParse(t, in).MarshalJSON()
		if err != nil || string(got) != haiku {
			t.Errorf("MarshalJSON of %s =\n%s, %v; want\n%s", in, got, err, haiku)
		}
	}
}

// A well-formed schedule with a condition M1 cannot compute is refused when it is saved, not priced wrongly later.
func TestParseScheduleRejects(t *testing.T) {
	tiers := `"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":10,"rates":{}},{"above":20,"rates":{}}]}`
	for name, edit := range map[string][2]string{
		"excess method":       {`"method":"whole"`, `"method":"excess"`},
		"unknown condition":   {`"v":1`, `"v":1,"time_of_day":{"from":"00:30","to":"08:30","rates":{"input":"0.5"}}`},
		"unknown tier member": {`"above":10,`, `"above":10,"per":"excess",`},
		"no basis":            {`"basis":["input"]`, `"basis":[]`},
		"no tiers":            {`"tiers":[{"above":10,"rates":{}},{"above":20,"rates":{}}]`, `"tiers":[]`},
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
		"lower-case currency": {`"USD"`, `"usd"`},
		"no text modality":    {`["text"]`, `["image"]`},
		"unknown modality":    {`["text"]`, `["text","pdf"]`},
		"modality twice":      {`["text"]`, `["text","text"]`},
	} {
		valid := `{"v":1,"currency":"USD","input_modalities":["text"],"rates":{"input":"0.000001"},` + tiers + `}`
		in := strings.Replace(valid, edit[0], edit[1], 1)
		if in == valid {
			t.Fatalf("%s: %q is not in the valid schedule", name, edit[0])
		}
		mustParse(t, valid)
		if _, err := catalog.ParseSchedule([]byte(in)); err == nil {
			t.Errorf("%s: ParseSchedule(%s) succeeded, want an error", name, in)
		}
	}
}
