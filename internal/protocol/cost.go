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
	amount := decimal(f.Cost)
	s := `,"cost":` + amount + `,"cost_currency":"` + f.Currency.String() + `"`
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
	if format == Embeddings || format == Rerank {
		return s + byok // the direct writers put the account's flag after the price
	}
	return byok + s
}

// decimal is an exact amount as the shortest plain decimal that holds it.
func decimal(e platform.Exact) string { return string(e.AppendDecimal(nil)) }
