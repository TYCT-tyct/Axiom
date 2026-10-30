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

// The row modes the catalog takes: the text modes it follows, and the image and audio modes whose per-piece,
// per-second and per-character items it prices.
const (
	modeChat = 1 << iota
	modeResponses
	modeImage
	modeSpeech
	modeTranscription
	modeText = modeChat | modeResponses
	modeAll  = modeText | modeImage | modeSpeech | modeTranscription
)

// litellmModes is the bit of each mode the catalog takes; a mode left out prices something it does not bill.
var litellmModes = map[string]int{
	"chat": modeChat, "responses": modeResponses,
	"image_generation": modeImage, "audio_speech": modeSpeech, "audio_transcription": modeTranscription,
}

// litellmItem is the item a field prices, in the row modes whose unit the field's name gives. A field is not read in a
// mode where its unit differs.
type litellmItem struct {
	item  Item
	modes int
}

// litellmItems are the fields that price one billing item. The other cost fields (batch prices, a monthly commitment,
// video with no item of its own, a duplicate spelling such as input_cost_per_token_cache_hit) price nothing we bill.
// output_cost_per_image is read only for image generation rows: chat rows carry the per-image-token price there, and
// on the size-prefixed image keys it is the price of generating one, not of an input image, so input_cost_per_image is
// not read at all.
var litellmItems = map[string]litellmItem{
	"input_cost_per_token":                      {ItemInput, modeAll},
	"output_cost_per_token":                     {ItemOutput, modeAll},
	"cache_read_input_token_cost":               {ItemCacheRead, modeAll},
	"cache_creation_input_token_cost":           {ItemCacheWrite, modeAll},
	"cache_creation_input_token_cost_above_1hr": {ItemCacheWrite1h, modeAll},
	"output_cost_per_reasoning_token":           {ItemReasoning, modeAll},
	"input_cost_per_audio_token":                {ItemAudioIn, modeChat | modeResponses | modeSpeech},
	"output_cost_per_audio_token":               {ItemAudioOut, modeChat | modeResponses | modeSpeech},
	"input_cost_per_image_token":                {ItemImageIn, modeChat | modeResponses | modeImage},
	"output_cost_per_image_token":               {ItemImageOut, modeChat | modeResponses | modeImage},
	"output_cost_per_image":                     {ItemImageOutPiece, modeImage},
	"input_cost_per_character":                  {ItemCharacterIn, modeSpeech},
	"output_cost_per_second":                    {ItemAudioOutSecond, modeSpeech},
	"input_cost_per_second":                     {ItemAudioInSecond, modeTranscription},
}

var (
	litellmTier    = regexp.MustCompile(`^(.+)_above_(\d+)k_tokens$`)
	litellmLevels  = map[string]ServiceTier{"_priority": ServiceTierPriority, "_flex": ServiceTierFlex}
	errNoVendorRow = errors.New("litellm: the table has no model of any vendor")
)

// ParseLiteLLM reads LiteLLM's model price table (model_prices_and_context_window.json) into the entries of the vendors
// the catalog follows, in the modes whose units it bills: chat, responses, image generation, speech and transcription.
// A field is read as: the service tier suffix (_priority, _flex), then the threshold suffix _above_<N>k_tokens (N
// thousand tokens), then the item, and only in the row modes the item's unit allows; a field that is no item after
// that, such as the _batches prices (batch is a supplier's alias, not a service tier), is not read. Numbers are taken
// from the text as written.
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
		kind, known := litellmModes[mode]
		if _, vendor := litellmProviders[provider]; !vendor || !known {
			continue
		}
		e := Entry{Key: key, Names: []string{strings.TrimPrefix(key, provider+"/")}, Text: kind&modeText != 0, Raw: table[key]}
		e.Err = readLiteLLMPrices(&e.sheet, kind, fields)
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, errNoVendorRow
	}
	return entries, nil
}

func readLiteLLMPrices(sh *sheet, mode int, fields map[string]jsonx.Raw) error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		st, above, it, ok := litellmField(name, mode)
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

// litellmField says which price a field name carries in a row of mode.
func litellmField(name string, mode int) (st ServiceTier, above int64, it Item, ok bool) {
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
	price, ok := litellmItems[name]
	if !ok || price.modes&mode == 0 {
		return 0, 0, 0, false
	}
	return st, above, price.item, true
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
