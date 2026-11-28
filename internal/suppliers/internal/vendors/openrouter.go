package vendors

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/direct"
)

// openRouter is how OpenRouter tells what a call may cost and what one did (docs/domains/suppliers-direct.md
// 「OpenRouter 的成本」): every provider of a model with its prices at /api/v1/models/{author}/{slug}/endpoints, and one
// generation's record at /api/v1/generation, by the id the X-Generation-Id header of every answer gives
// (OpenRouter《Models API》《API Reference》).
var openRouter = &direct.Aggregator{
	GenerationHeader: "X-Generation-Id",
	CeilingPath:      openRouterEndpoints,
	Ceiling:          openRouterCeiling,
	GenerationPath:   func(id string) string { return "/api/v1/generation?id=" + url.QueryEscape(id) },
	Generation:       openRouterGeneration,
}

// openRouterEndpoints is where a model's providers are listed, "" for a name not to read: one that is not
// author/slug, or a variant (:free, :nitro), which is priced apart from the model.
func openRouterEndpoints(name string) string {
	author, slug, ok := strings.Cut(name, "/")
	if !ok || author == "" || slug == "" || strings.ContainsAny(name, ":?#") {
		return ""
	}
	return "/api/v1/models/" + url.PathEscape(author) + "/" + url.PathEscape(slug) + "/endpoints"
}

// openRouterItems maps each price field of an endpoint, in USD per token or per use, onto the items it bounds. A
// cache write whose time to live a Chat reply does not tell is bounded at either write price, so both write fields
// bound both items; cached audio is a cache read; image fields are per image token, as the list's prices show
// (catalog's openrouterItems).
var openRouterItems = map[string][]catalog.Item{
	"prompt":               {catalog.ItemInput},
	"completion":           {catalog.ItemOutput},
	"input_cache_read":     {catalog.ItemCacheRead},
	"input_audio_cache":    {catalog.ItemCacheRead},
	"input_cache_write":    {catalog.ItemCacheWrite, catalog.ItemCacheWrite1h},
	"input_cache_write_1h": {catalog.ItemCacheWrite, catalog.ItemCacheWrite1h},
	"internal_reasoning":   {catalog.ItemReasoning},
	"web_search":           {catalog.ItemWebSearch},
	"request":              {catalog.ItemRequest},
	"audio":                {catalog.ItemAudioIn},
	"image":                {catalog.ItemImageIn},
	"image_token":          {catalog.ItemImageIn},
	"image_output":         {catalog.ItemImageOut},
	"audio_output":         {catalog.ItemAudioOut},
}

// openRouterCeiling is the highest price of each item over every provider of the model and every one of its price
// overrides (the long-context tiers among them), so no tier's boundary needs reading. A discount is left out: the price
// before it is never below the price after. A price field the table does not know, at a price above zero, is an item
// the schedule cannot express; a list without a provider is a model nobody serves.
func openRouterCeiling(body []byte) adapter.Ceiling {
	var list struct {
		Data struct {
			Endpoints []struct {
				Pricing map[string]jsonx.Raw `json:"pricing"`
			} `json:"endpoints"`
		} `json:"data"`
	}
	if err := jsonx.Unmarshal(body, &list); err != nil {
		return adapter.Ceiling{}
	}
	if len(list.Data.Endpoints) == 0 {
		return adapter.Ceiling{Outcome: adapter.CeilingNone}
	}
	highest := map[catalog.Item]platform.Exact{}
	for _, e := range list.Data.Endpoints {
		prices := []map[string]jsonx.Raw{e.Pricing}
		var overrides []map[string]jsonx.Raw
		if o, ok := e.Pricing["overrides"]; ok && jsonx.Unmarshal(o, &overrides) != nil {
			return adapter.Ceiling{Outcome: adapter.CeilingUnexpressible}
		}
		for _, p := range append(prices, overrides...) {
			if !raise(highest, p) {
				return adapter.Ceiling{Outcome: adapter.CeilingUnexpressible}
			}
		}
	}
	schedule, err := ceilingSchedule(highest)
	if err != nil {
		return adapter.Ceiling{Outcome: adapter.CeilingUnexpressible}
	}
	return adapter.Ceiling{Outcome: adapter.CeilingRead, Schedule: schedule}
}

// raise takes each price of p above the highest so far. Only string members are prices; the others are a discount, a
// tier's threshold or the overrides themselves. It is false for a price that does not read, or one of a field the
// table does not know that is above zero.
func raise(highest map[catalog.Item]platform.Exact, p map[string]jsonx.Raw) bool {
	for name, v := range p {
		if v.Kind() != '"' {
			continue
		}
		price, ok := priceOf(v)
		items, known := openRouterItems[name]
		if !ok || !known && price.Cmp(platform.Exact{}) != 0 {
			return false
		}
		for _, it := range items {
			if was, ok := highest[it]; !ok || price.Cmp(was) > 0 {
				highest[it] = price
			}
		}
	}
	return true
}

// priceOf reads a price, a JSON string of a decimal from 0 up.
func priceOf(v jsonx.Raw) (platform.Exact, bool) {
	var text string
	if jsonx.Unmarshal(v, &text) != nil {
		return platform.Exact{}, false
	}
	price, err := platform.CeilDecimal(text)
	return price, err == nil && price.Cmp(platform.Exact{}) >= 0
}

// ceilingSchedule is the prices as a schedule in USD, through the schedule's own reading, which holds every price to
// its range.
func ceilingSchedule(prices map[catalog.Item]platform.Exact) (catalog.Schedule, error) {
	if _, ok := prices[catalog.ItemOutput]; !ok {
		return catalog.Schedule{}, errors.New("no output price")
	}
	rates := make(map[string]string, len(prices))
	for it, p := range prices {
		rates[it.String()] = string(p.AppendDecimal(nil))
	}
	b, err := jsonx.MarshalCanonical(map[string]any{"v": 1, "currency": "USD", "rates": rates})
	if err != nil {
		return catalog.Schedule{}, err
	}
	return catalog.ParseSchedule(b)
}

// openRouterGeneration reads a generation's record: its total_cost in credits and whether it ran on a provider key of
// the account's.
func openRouterGeneration(body []byte) (adapter.Generation, error) {
	var g struct {
		Data struct {
			TotalCost jsonx.Raw `json:"total_cost"`
			IsBYOK    bool      `json:"is_byok"`
		} `json:"data"`
	}
	if err := jsonx.Unmarshal(body, &g); err != nil {
		return adapter.Generation{}, err
	}
	cost := g.Data.TotalCost
	if cost.Kind() != '0' || cost[0] == '-' {
		return adapter.Generation{}, fmt.Errorf("total_cost %q is not a cost", cost)
	}
	amount, err := platform.CeilDecimal(string(cost))
	if err != nil {
		return adapter.Generation{}, fmt.Errorf("total_cost: %w", err)
	}
	return adapter.Generation{Found: true, Cost: amount, BYOK: g.Data.IsBYOK}, nil
}
