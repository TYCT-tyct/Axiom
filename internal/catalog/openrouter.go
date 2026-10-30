package catalog

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"axiom/internal/platform/jsonx"
)

// openrouterItems are the pricing fields whose unit is known: tokens, or searches for web_search (Anthropic's $0.01 a
// search is "0.01" there). The per-image fields are not read: their published definition is per input or output image,
// but the list prices Gemini's prompt and image the same and a generated image at 0.00003, which is the price of one
// image token, so a per-piece price taken from it would be a fraction of the cost. Per-request fields are another unit
// again and are not read here.
var openrouterItems = map[string]Item{
	"prompt":               ItemInput,
	"completion":           ItemOutput,
	"input_cache_read":     ItemCacheRead,
	"input_cache_write":    ItemCacheWrite,
	"input_cache_write_1h": ItemCacheWrite1h,
	"internal_reasoning":   ItemReasoning,
	"web_search":           ItemWebSearch,
}

var errOverrides = errors.New("the entry has price overrides (long prompts or hours of the day), whose thresholds and usage basis are not documented")

// ParseOpenRouter reads OpenRouter's model list (/api/v1/models). A model matches by its id and its canonical_slug,
// which carries the vendor's date. :free is a model of its own, with its own prices and limits, so it is kept; other
// variants such as :batch are priced apart from the model and are left out. Prices with overrides are not used: the
// field names a minimum prompt length but not what counts toward it or whether the minimum itself is in the tier, and
// some overrides are by hour.
func ParseOpenRouter(body []byte) ([]Entry, error) {
	var list struct {
		Data []jsonx.Raw `json:"data"`
	}
	if err := jsonx.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	var entries []Entry
	for _, raw := range list.Data {
		var m struct {
			ID           string `json:"id"`
			CanonicalID  string `json:"canonical_slug"`
			Architecture struct {
				Output []string `json:"output_modalities"`
			} `json:"architecture"`
			Pricing map[string]jsonx.Raw `json:"pricing"`
		}
		if err := jsonx.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("openrouter: a model: %w", err)
		}
		norm := Normalize(m.ID)
		if m.ID == "" || (strings.Contains(norm, ":") && !freeName(norm)) {
			continue
		}
		evidence, err := jsonx.MarshalCanonical(map[string]any{"id": m.ID, "canonical_slug": m.CanonicalID, "pricing": m.Pricing})
		if err != nil {
			return nil, err
		}
		names := slices.DeleteFunc([]string{m.ID, m.CanonicalID}, func(n string) bool { return n == "" })
		if freeName(norm) {
			// The canonical slug is the paid model's name: keeping it would let the free entry's zero price match the
			// paid model in a dataset sync. Only the :free name identifies this entry.
			names = []string{m.ID}
		}
		e := Entry{Key: m.ID, Names: names, Text: slices.Equal(m.Architecture.Output, []string{"text"}), Raw: evidence}
		e.Err = readOpenRouterPrices(&e.sheet, m.Pricing)
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, errors.New("openrouter: the list has no model")
	}
	return entries, nil
}

func readOpenRouterPrices(sh *sheet, pricing map[string]jsonx.Raw) error {
	if o, ok := pricing["overrides"]; ok && string(o) != "[]" && string(o) != "null" {
		return errOverrides
	}
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(pricing)) {
		it, ok := openrouterItems[name]
		if !ok {
			continue
		}
		price, err := numberText(pricing[name])
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		sh.set(ServiceTierStandard, 0, it, price)
	}
	return errors.Join(errs...)
}
