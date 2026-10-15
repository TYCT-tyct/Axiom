package newapi

import (
	"context"
	"fmt"
	"math/big"
	"net/url"
	"slices"
	"strings"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// priceEntry is the part of a /api/pricing entry that is read. A ratio left out of the answer is left out here, which
// is not the same as a ratio of zero.
type priceEntry struct {
	Model           string   `json:"model_name"`
	QuotaType       int      `json:"quota_type"`
	BillingMode     string   `json:"billing_mode"`
	ModelRatio      number   `json:"model_ratio"`
	CompletionRatio *number  `json:"completion_ratio"`
	CacheRatio      *number  `json:"cache_ratio"`
	CreateCache     *number  `json:"create_cache_ratio"`
	ImageRatio      *number  `json:"image_ratio"`
	Endpoints       []string `json:"supported_endpoint_types"`
}

// Offer implements adapter.Manager: the account's groups with their ratios, the models each serves, and for each model
// a cost schedule where new-api's pricing can be expressed as one. The pricing answer is not trusted to name the
// models a group serves; the per-group model list does.
func (m *manager) Offer(ctx context.Context) (adapter.Offer, error) {
	var groups struct {
		Data map[string]struct {
			Ratio number `json:"ratio"`
			Desc  string `json:"desc"`
		} `json:"data"`
	}
	if err := m.api.get(ctx, "/api/user/self/groups", global, &groups); err != nil {
		return adapter.Offer{}, err
	}
	var pricing struct {
		Data []priceEntry `json:"data"`
	}
	if err := m.api.get(ctx, "/api/pricing", global, &pricing); err != nil {
		return adapter.Offer{}, err
	}
	prices := make(map[string]priceEntry, len(pricing.Data))
	for _, e := range pricing.Data {
		prices[e.Model] = e
	}
	var offer adapter.Offer
	for _, name := range sortedKeys(groups.Data) {
		ratio, ok := groups.Data[name].Ratio.rat()
		if !ok { // "auto" picks among groups per request and has no ratio of its own
			continue
		}
		var names struct {
			Data []string `json:"data"`
		}
		if err := m.api.get(ctx, "/api/user/models?group="+url.QueryEscape(name), global, &names); err != nil {
			return adapter.Offer{}, err
		}
		g, err := buildGroup(name, groups.Data[name].Desc, groups.Data[name].Ratio, ratio, names.Data, prices)
		if err != nil {
			return adapter.Offer{}, err
		}
		offer.Groups = append(offer.Groups, g)
	}
	return offer, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func buildGroup(name, desc string, ratioText number, ratio *big.Rat, models []string, prices map[string]priceEntry) (adapter.Group, error) {
	slices.Sort(models)
	g := adapter.Group{Ref: name, Name: name, Ratio: adapter.Amount(ratioText)}
	endpoints := map[string]bool{}
	for _, upstream := range slices.Compact(models) {
		entry, priced := prices[upstream]
		model := adapter.Model{UpstreamName: upstream, Formats: formatsOf(entry.Endpoints)}
		if priced {
			model.Cost = schedule(entry, ratio)
			model.Without = unsupported(entry)
			for _, e := range entry.Endpoints {
				endpoints[e] = true
			}
		}
		g.Models = append(g.Models, model)
	}
	facts, err := jsonx.MarshalCanonical(map[string]any{"desc": desc, "endpoint_types": sortedKeys(endpoints)})
	if err != nil {
		return adapter.Group{}, fmt.Errorf("new-api: encode the facts of group %q: %w", name, err)
	}
	g.Facts = facts
	return g, nil
}

// formatsOf maps new-api's endpoint types to call formats; a model that names none is served as OpenAI chat.
func formatsOf(endpoints []string) []protocol.Format {
	var formats []protocol.Format
	for _, e := range endpoints {
		if f, ok := endpointFormats[e]; ok && !slices.Contains(formats, f) {
			formats = append(formats, f)
		}
	}
	if len(formats) == 0 {
		return []protocol.Format{protocol.Chat}
	}
	return formats
}

var endpointFormats = map[string]protocol.Format{
	"openai": protocol.Chat, "openai-response": protocol.Responses, "anthropic": protocol.Messages,
}

// unsupported are the capabilities the price does not cover. An image ratio of its own other than 1 means images are
// priced apart from text, which a schedule's input price cannot say, so the endpoint does not claim image input.
func unsupported(e priceEntry) protocol.Capabilities {
	if e.ImageRatio != nil {
		if r, ok := e.ImageRatio.rat(); !ok || r.Cmp(big.NewRat(1, 1)) != 0 {
			return protocol.Images
		}
	}
	return 0
}

// schedule is the model's cost as a catalog schedule in new-api's own unit, quota per token, with the currency XXX
// ("no currency"): the amounts it yields are quota, not money. It is nil, which makes the cost unknown, for what a
// schedule cannot express: a price per call, an expression, a ratio that is missing or not a number, or a price above
// what a unit price may be. Rounding goes up, so a price is never lower than new-api's.
//
// new-api charges (prompt + completion × completion_ratio) × model_ratio × group_ratio, a cache read at cache_ratio of
// the input price (1 when the pricing does not say), a cache write at create_cache_ratio of it (1.25 by default) and
// an hour-long cache write at 1.6 times that (constants in new-api's code).
func schedule(e priceEntry, group *big.Rat) *catalog.Schedule {
	if e.BillingMode == "tiered_expr" || e.QuotaType != 0 {
		return nil
	}
	model, ok := e.ModelRatio.rat()
	if !ok {
		return nil
	}
	input := new(big.Rat).Mul(model, group)
	cacheWrite := new(big.Rat).Mul(input, ratioOr(e.CreateCache, big.NewRat(5, 4)))
	prices := map[string]*big.Rat{
		"input":          input,
		"output":         new(big.Rat).Mul(input, ratioOr(e.CompletionRatio, big.NewRat(1, 1))),
		"cache_read":     new(big.Rat).Mul(input, ratioOr(e.CacheRatio, big.NewRat(1, 1))),
		"cache_write":    cacheWrite,
		"cache_write_1h": new(big.Rat).Mul(cacheWrite, big.NewRat(8, 5)),
	}
	rates := make(map[string]string, len(prices))
	for item, p := range prices {
		text, ok := unitPrice(p)
		if !ok {
			return nil
		}
		rates[item] = text
	}
	stored, err := jsonx.MarshalCanonical(map[string]any{"v": 1, "currency": "XXX", "rates": rates})
	if err != nil {
		return nil
	}
	s, err := catalog.ParseSchedule(stored)
	if err != nil {
		return nil
	}
	return &s
}

// ratioOr is the ratio n holds, or fallback when the pricing leaves it out.
func ratioOr(n *number, fallback *big.Rat) *big.Rat {
	if n == nil {
		return fallback
	}
	if r, ok := n.rat(); ok {
		return r
	}
	return new(big.Rat).SetInt64(-1) // a ratio that is no number: unitPrice refuses the negative result
}

// unitPrice writes p rounded up to 15 decimals, the precision of a unit price, or reports that it is negative or
// above 1000, which a unit price cannot be.
func unitPrice(p *big.Rat) (string, bool) {
	if p.Sign() < 0 {
		return "", false
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(15), nil)
	scaled := new(big.Rat).Mul(p, new(big.Rat).SetInt(scale))
	n := new(big.Int).Div(scaled.Num(), scaled.Denom()) // floor, as the numerator is not negative
	if new(big.Rat).SetInt(n).Cmp(scaled) < 0 {
		n.Add(n, big.NewInt(1))
	}
	whole, frac := new(big.Int).DivMod(n, scale, new(big.Int))
	if whole.Cmp(big.NewInt(1000)) > 0 || whole.Cmp(big.NewInt(1000)) == 0 && frac.Sign() > 0 {
		return "", false
	}
	text := whole.String()
	if frac.Sign() > 0 {
		digits := fmt.Sprintf("%015s", frac.String())
		text += "." + strings.TrimRight(strings.ReplaceAll(digits, " ", "0"), "0")
	}
	return text, true
}
