package protocol

import (
	"axiom/internal/platform"
)

// cost is the members an ending's usage gets for f, each led by a comma: none without a settled cost. Chat and
// Responses also get the parts OpenRouter reports beside the price: the server tools' cost and the cache discount.
//
//nolint:cyclop // one function decides the members every format's usage gets; the branches are the formats
func cost(f *Final, format Format) string {
	byok := ""
	if format != Messages { // Messages' usage has no such member (docs/domains/gateway.md「用量与费用」)
		byok = `,"is_byok":false`
		if f != nil && f.IsBYOK {
			byok = `,"is_byok":true`
		}
	}
	if f == nil || f.Pending {
		return byok
	}
	// The account's flag leads the price for the formats the relay assembles and trails it for the direct writers.
	// Either way it is an operand of this concatenation: appending it afterwards costs one allocation per rendered
	// usage, which the aggregate and relay benchmarks count (scripts/ci/bench-compare.sh).
	lead, trail := byok, ""
	if format == Embeddings || format == Rerank {
		lead, trail = "", byok
	}
	s := lead + `,"cost":` + decimal(f.Cost) + `,"cost_currency":"` + f.Currency.String() + `"` + trail
	if f.Estimated {
		s += `,"cost_estimated":true`
	}
	if format == Chat || format == Responses {
		if f.ServerToolCost != nil {
			s += `,"cost_details":{"server_tool_cost":` + decimal(*f.ServerToolCost) + `}`
		}
		if f.CacheDiscount.Cmp(platform.Exact{}) > 0 {
			s += `,"cache_discount":` + decimal(f.CacheDiscount)
		}
	}
	return s
}

// decimal is an exact amount as the shortest plain decimal that holds it.
func decimal(e platform.Exact) string { return string(e.AppendDecimal(nil)) }
