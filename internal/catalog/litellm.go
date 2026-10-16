package catalog

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"axiom/internal/platform/jsonx"
)

// litellmProviders are the LiteLLM providers that are the vendor itself, not a cloud that resells it at its own
// prices (vertex_ai, bedrock, azure), and the vendor's name in the catalog.
var litellmProviders = map[string]string{"openai": "openai", "anthropic": "anthropic", "gemini": "google", "deepseek": "deepseek"}

// litellmItems are the fields that price one billing item. The other cost fields (per image, per second, per
// character, batch prices, a duplicate spelling such as input_cost_per_token_cache_hit) price nothing we bill.
var litellmItems = map[string]Item{
	"input_cost_per_token":                      ItemInput,
	"output_cost_per_token":                     ItemOutput,
	"cache_read_input_token_cost":               ItemCacheRead,
	"cache_creation_input_token_cost":           ItemCacheWrite,
	"cache_creation_input_token_cost_above_1hr": ItemCacheWrite1h,
	"output_cost_per_reasoning_token":           ItemReasoning,
	"input_cost_per_audio_token":                ItemAudioIn,
	"output_cost_per_audio_token":               ItemAudioOut,
	"input_cost_per_image_token":                ItemImageIn,
	"output_cost_per_image_token":               ItemImageOut,
}

var (
	litellmTier    = regexp.MustCompile(`^(.+)_above_(\d+)k_tokens$`)
	litellmLevels  = map[string]ServiceTier{"_priority": ServiceTierPriority, "_flex": ServiceTierFlex}
	errNoVendorRow = errors.New("litellm: the table has no text model of any vendor")
)

// ParseLiteLLM reads LiteLLM's model price table (model_prices_and_context_window.json) into the entries of the vendors
// the catalog follows. A field is read as: the service tier suffix (_priority, _flex), then the threshold suffix
// _above_<N>k_tokens (N thousand tokens), then the item; a field that is no item after that, such as the _batches
// prices (batch is a supplier's alias, not a service tier), is not read. Numbers are taken from the text as written.
func ParseLiteLLM(body []byte) ([]Entry, error) {
	var table map[string]jsonx.Raw
	if err := jsonx.Unmarshal(body, &table); err != nil {
		return nil, fmt.Errorf("litellm: %w", err)
	}
	var entries []Entry
	for _, key := range slices.Sorted(maps.Keys(table)) {
		var fields map[string]jsonx.Raw
		if jsonx.Unmarshal(table[key], &fields) != nil {
			continue // not an object, so not a model
		}
		var provider, mode string
		if jsonx.Unmarshal(fields["litellm_provider"], &provider) != nil || jsonx.Unmarshal(fields["mode"], &mode) != nil {
			continue // sample_spec and the like describe the format
		}
		if _, vendor := litellmProviders[provider]; !vendor || mode != "chat" && mode != "responses" {
			continue
		}
		e := Entry{Key: key, Names: []string{strings.TrimPrefix(key, provider+"/")}, Text: true, Raw: table[key]}
		e.Err = readLiteLLMPrices(&e.sheet, fields)
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, errNoVendorRow
	}
	return entries, nil
}

func readLiteLLMPrices(sh *sheet, fields map[string]jsonx.Raw) error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		st, above, it, ok := litellmField(name)
		if !ok {
			continue
		}
		price, err := numberText(fields[name])
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		sh.set(st, above, it, price)
	}
	if price, ok := searchPrice(fields["search_context_cost_per_query"]); ok {
		sh.set(ServiceTierStandard, 0, ItemWebSearch, price)
	}
	return errors.Join(errs...)
}

// litellmField says which price a field name carries.
func litellmField(name string) (st ServiceTier, above int64, it Item, ok bool) {
	for suffix, tier := range litellmLevels {
		if base, found := strings.CutSuffix(name, suffix); found {
			name, st = base, tier
		}
	}
	if m := litellmTier.FindStringSubmatch(name); m != nil {
		n, err := strconv.ParseInt(m[2], 10, 32)
		if err != nil || n == 0 {
			return 0, 0, 0, false
		}
		name, above = m[1], n*1000
	}
	it, ok = litellmItems[name]
	return st, above, it, ok
}

// searchPrice is the price of a web search when LiteLLM gives one price for every context size; sizes priced
// differently are not one price per search, so none is taken and a model that reports searches is held as missing it.
func searchPrice(raw jsonx.Raw) (string, bool) {
	var sizes map[string]jsonx.Raw
	if raw == nil || jsonx.Unmarshal(raw, &sizes) != nil {
		return "", false
	}
	var prices []string
	for _, size := range slices.Sorted(maps.Keys(sizes)) {
		p, err := numberText(sizes[size])
		if err != nil {
			return "", false
		}
		prices = append(prices, p)
	}
	slices.Sort(prices)
	if prices = slices.Compact(prices); len(prices) != 1 {
		return "", false
	}
	return prices[0], true
}

// numberText reads a JSON number, or a string holding one, as plain decimal text.
func numberText(raw jsonx.Raw) (string, error) {
	var s string
	if len(raw) > 0 && raw[0] == '"' {
		if err := jsonx.Unmarshal(raw, &s); err != nil {
			return "", err
		}
	} else {
		s = string(raw)
	}
	return plainDecimal(s)
}
