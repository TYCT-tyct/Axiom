package protocol_test

import (
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

// usageCase is one complete reply's usage as UsageOf reads it: the counts by item, the tier, and whether the reply is
// one protocol must refuse.
type usageCase struct {
	name                     string
	body                     string
	tier                     catalog.ServiceTier
	input, cache, out, think int64
	bad                      bool
}

// TestUsageOf: the usage of a complete Responses reply is read as its stream's ending is: the input total split into
// its parts, the output into reasoning, the service tier it served at, and which of them were reported; a reply that
// names none used nothing of the model, and one protocol cannot map is invalid, which billing estimates rather than
// waiving (docs/domains/billing.md「免单规则」).
func TestUsageOf(t *testing.T) {
	const usage = `"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},` +
		`"output_tokens":10,"output_tokens_details":{"reasoning_tokens":4},"total_tokens":110}`
	for _, c := range []usageCase{
		{
			name:  "input, cache read, output and reasoning",
			body:  `{"object":"response.compaction",` + usage + `}`,
			input: 60,
			cache: 40,
			out:   6,
			think: 4,
		},
		{
			name:  "a tier named after the usage",
			body:  `{` + usage + `,"service_tier":"priority"}`,
			tier:  catalog.ServiceTierPriority,
			input: 60,
			cache: 40,
			out:   6,
			think: 4,
		},
		{
			name: "no usage at all",
			body: `{"object":"response.compaction","output":[]}`,
		},
		{
			name: "parts over the total",
			body: `{"usage":{"input_tokens":1,"input_tokens_details":{"cached_tokens":2}}}`,
			bad:  true,
		},
		{
			name: "not JSON",
			body: `{"usage":`,
			bad:  true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := protocol.UsageOf([]byte(c.body))
			if c.bad {
				if u.Invalid == nil {
					t.Fatalf("the usage is valid: %+v", u)
				}
				return
			}
			checkUsage(t, c, u)
		})
	}
}

// checkUsage: the reply's counts and tier, and which of the counts were reported.
func checkUsage(t *testing.T, c usageCase, u protocol.Usage) {
	t.Helper()
	if u.Invalid != nil || len(u.Segments) != 1 {
		t.Fatalf("usage %+v", u)
	}
	s := u.Segments[0]
	if got := s.Usage.ServiceTier(); got != c.tier {
		t.Errorf("tier %v, want %v", got, c.tier)
	}
	for _, it := range []struct {
		item catalog.Item
		want int64
	}{
		{item: catalog.ItemInput, want: c.input},
		{item: catalog.ItemCacheRead, want: c.cache},
		{item: catalog.ItemOutput, want: c.out},
		{item: catalog.ItemReasoning, want: c.think},
	} {
		if got := s.Usage.Count(it.item); got != it.want {
			t.Errorf("%s is %d, want %d", it.item, got, it.want)
		}
		if got := s.Reported.Has(it.item); got != (it.want > 0) {
			t.Errorf("%s reported %t", it.item, got)
		}
	}
}
