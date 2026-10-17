package protocol_test

import (
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
)

// TestUsageOf: the usage of a complete Responses reply is read as its stream's ending is: the input total split into
// its parts, the output into reasoning, the service tier it served at, and which of them were reported; a reply that
// names none used nothing of the model, and one protocol cannot map is invalid, which billing estimates rather than
// waiving (docs/domains/billing.md「免单规则」).
func TestUsageOf(t *testing.T) {
	const usage = `"usage":{"input_tokens":100,"input_tokens_details":{"cached_tokens":40},` +
		`"output_tokens":10,"output_tokens_details":{"reasoning_tokens":4},"total_tokens":110}`
	for _, c := range []struct {
		name  string
		body  string
		tier  catalog.ServiceTier
		input int64
		cache int64
		out   int64
		think int64
		bad   bool
	}{
		{"input, cache read, output and reasoning", `{"object":"response.compaction",` + usage + `}`, catalog.ServiceTierStandard, 60, 40, 6, 4, false},
		{"a tier named after the usage", `{` + usage + `,"service_tier":"priority"}`, catalog.ServiceTierPriority, 60, 40, 6, 4, false},
		{"no usage at all", `{"object":"response.compaction","output":[]}`, catalog.ServiceTierStandard, 0, 0, 0, 0, false},
		{"parts over the total", `{"usage":{"input_tokens":1,"input_tokens_details":{"cached_tokens":2}}}`, 0, 0, 0, 0, 0, true},
		{"not JSON", `{"usage":`, 0, 0, 0, 0, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := protocol.UsageOf([]byte(c.body))
			if c.bad {
				if u.Invalid == nil {
					t.Fatalf("the usage is valid: %+v", u)
				}
				return
			}
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
			}{{catalog.ItemInput, c.input}, {catalog.ItemCacheRead, c.cache}, {catalog.ItemOutput, c.out}, {catalog.ItemReasoning, c.think}} {
				if got := s.Usage.Count(it.item); got != it.want {
					t.Errorf("%s is %d, want %d", it.item, got, it.want)
				}
				if got := s.Reported.Has(it.item); got != (it.want > 0) {
					t.Errorf("%s reported %t", it.item, got)
				}
			}
		})
	}
}
