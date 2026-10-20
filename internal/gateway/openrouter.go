package gateway

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
)

// The model list in OpenRouter's shape (docs/openrouter-parity.md §2): the models GET /v1/models lists, with the price
// the site sells each at and the limits, modalities and parameters its spec declares. It is the same list, so the two
// prefixes cannot disagree about which models a key may call; only the members differ, and /v1 stays in OpenAI's and
// Anthropic's shapes for the clients that read those.

// orListPath is where the list is served and what its links point at.
const orListPath = "/api/v1/models"

// orMaxLimit is the most models one page may hold: the maximum of OpenRouter's limit parameter (OpenAPI `/models`).
const orMaxLimit = 1000

// listOpenRouterModels answers GET /api/v1/models in OpenRouter's shape: every model this key may call that routing
// serves, with the price the site sells it at, filtered and paged by the query parameters Axiom keeps.
func (h *Handler) listOpenRouterModels(w http.ResponseWriter, r *http.Request) {
	format := formatFor(r)
	release, ok := h.ipSlot(w, r, format)
	if !ok {
		return
	}
	defer release()
	v, err := h.visible(r)
	var q orQuery
	var l *openRouterListing
	if err == nil {
		if q, err = readORQuery(r); err == nil {
			l, err = h.openRouterFor(r.Context(), v.snap)
		}
	}
	if err != nil {
		h.fail(w, r, format, err)
		return
	}
	writeBody(w, l.encode(v, q)) // no-store: the list is this key's, and its prices are the site's
}

// retrieveOpenRouterModel answers GET /api/v1/models/{model} in OpenRouter's shape (spec `ModelResponse`): the object the
// list shows for the model the name resolves to, under the model's own id whichever of its names was asked for, because
// OpenRouter's shape has one id per model. A name the catalog does not know, a model nothing serves, one this key may not
// use and one whose spec or price cannot fill the object are the same 404 the list leaves them out with.
func (h *Handler) retrieveOpenRouterModel(w http.ResponseWriter, r *http.Request) {
	format := formatFor(r)
	release, ok := h.ipSlot(w, r, format)
	if !ok {
		return
	}
	defer release()
	var entry orEntry
	v, err := h.visible(r)
	if err == nil {
		entry, err = h.openRouterEntry(r.Context(), v, r.PathValue("model"))
	}
	if err != nil {
		h.fail(w, r, format, err)
		return
	}
	writeJSON(w, orModelResponse{Data: entry})
}

// openRouterEntry is the model the name resolves to, as the list holds it.
func (h *Handler) openRouterEntry(ctx context.Context, v visibility, name string) (orEntry, error) {
	resolved, err := v.snap.ResolvePublic(name)
	if err != nil {
		return orEntry{}, err
	}
	if !v.shows(resolved.Model) {
		return orEntry{}, errNoModel
	}
	l, err := h.openRouterFor(ctx, v.snap)
	if err != nil {
		return orEntry{}, err
	}
	for i := range l.models {
		if l.models[i].model == resolved.Model {
			return l.models[i].orEntry, nil
		}
	}
	return orEntry{}, errNoModel
}

// openRouterListing is the catalog in OpenRouter's shape at one reading of the sale prices, encoded once per model: what
// a request pays is keeping the entries its key sees and its query keeps, and copying the page out. It is rebuilt when
// the catalog snapshot changes and when its prices reading is as old as a routing snapshot's (rebuildEvery): routing
// prices a call at that same reading, so the list never shows a price a call is not charged at.
type openRouterListing struct {
	snap   *catalog.Snapshot
	read   time.Time
	models []orModel // in the snapshot's order
}

// openRouterFor is the list of the snapshot at the sale prices of the moment, built when there is none for this snapshot
// or the prices are due a reading. Two requests that build it at once make the same list.
func (h *Handler) openRouterFor(ctx context.Context, snap *catalog.Snapshot) (*openRouterListing, error) {
	if l := h.openRouter.Load(); l != nil && l.snap == snap && time.Since(l.read) < rebuildEvery {
		return l, nil
	}
	prices, err := h.Prices(ctx)
	if err != nil {
		return nil, err
	}
	l, err := newOpenRouterListing(snap, prices)
	if err != nil {
		return nil, err
	}
	h.openRouter.Store(l)
	return l, nil
}

// newOpenRouterListing lists every model the catalog lists that routing can serve and a rule prices, one entry each. A
// model without a sale price is left out for the reason routing leaves its endpoints out, and one whose spec does not
// declare its limits or whose price cannot fill prompt and completion is left out because OpenRouter's object cannot be
// written without them.
func newOpenRouterListing(snap *catalog.Snapshot, prices map[catalog.ModelID]billing.Sold) (*openRouterListing, error) {
	l := &openRouterListing{snap: snap, read: time.Now()}
	for m := range snap.Models() {
		serving, serves := m.Serving()
		sold, priced := prices[m.ID]
		if !serves || !priced {
			continue
		}
		e, ok, err := newORModel(m.Slug, m, serving, sold.Schedule)
		if err != nil {
			return nil, err
		}
		if ok {
			l.models = append(l.models, e)
		}
	}
	return l, nil
}

// orModel is one model of the list: the object as OpenRouter's shape has it, encoded once, and what the query filters
// and orders by, so a request reads neither a spec nor a price again.
type orModel struct {
	orEntry
	body    []byte
	model   catalog.ModelID
	author  string
	search  string // the catalog name folded, for q
	prompt  platform.UnitPrice
	output  platform.UnitPrice
	average platform.Exact // prompt + completion: the average the price orderings compare, undivided
}

// orModelResponse is OpenRouter's answer for one model (spec `ModelResponse`).
type orModelResponse struct {
	Data orEntry `json:"data"`
}

// orEntry is one model as OpenRouter's model object has it (spec `Model`): the members the catalog's spec and the price
// schedule fill, null for the required ones Axiom has no data for, and none of the optional ones it cannot fill
// (description, hugging_face_id, knowledge_cutoff, expiration_date, alias_target, benchmarks). A member made up to fill
// the shape would be read as data.
type orEntry struct {
	ID                  string         `json:"id"`
	CanonicalSlug       string         `json:"canonical_slug"`
	Name                string         `json:"name"`
	Created             int64          `json:"created"`
	Pricing             orPricing      `json:"pricing"`
	ContextLength       int64          `json:"context_length"`
	Architecture        orArchitecture `json:"architecture"`
	TopProvider         orTopProvider  `json:"top_provider"`
	PerRequestLimits    any            `json:"per_request_limits"`
	SupportedParameters []string       `json:"supported_parameters"`
	DefaultParameters   any            `json:"default_parameters"`
	SupportedVoices     any            `json:"supported_voices"`
	Reasoning           *orReasoning   `json:"reasoning,omitempty"`
	Links               orLinks        `json:"links"`
}

// orPricing is OpenRouter's price list (spec `PublicPricing`), one member per item whose unit one of its members has:
// dollars per token in the site's currency, which for the main site is the dollar, except web_search, which is per
// search. image and image_output are per image, request is per request, input_audio_cache is per cached audio token, and
// discount and overrides have no counterpart in a sale schedule, so the members Axiom cannot fill are left out rather
// than written as zero, which would read as free. image_token and internal_reasoning are shown for the models that take
// images or reason (newORPricing). The prices are the standard service tier's below the first context threshold: the
// price the shape means by a model's price under default conditions.
type orPricing struct {
	Prompt            string `json:"prompt"`
	Completion        string `json:"completion"`
	InputCacheRead    string `json:"input_cache_read,omitempty"`
	InputCacheWrite   string `json:"input_cache_write,omitempty"`
	InputCacheWrite1h string `json:"input_cache_write_1h,omitempty"`
	InternalReasoning string `json:"internal_reasoning,omitempty"`
	ImageToken        string `json:"image_token,omitempty"`
	Audio             string `json:"audio,omitempty"`
	AudioOutput       string `json:"audio_output,omitempty"`
	WebSearch         string `json:"web_search,omitempty"`
}

// orArchitecture is OpenRouter's modalities (spec `ModelArchitecture`). tokenizer and instruct_type are left out: the
// catalog records neither.
type orArchitecture struct {
	Modality         string   `json:"modality"`
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
}

// orTopProvider is OpenRouter's top provider (spec `TopProviderInfo`): Axiom sells a model at one price and one limit, so
// the model's own are the top provider's. is_moderated is false: the gateway does not moderate content.
type orTopProvider struct {
	ContextLength       int64 `json:"context_length"`
	MaxCompletionTokens int64 `json:"max_completion_tokens"`
	IsModerated         bool  `json:"is_moderated"`
}

// orReasoning is OpenRouter's reasoning metadata (spec `ModelReasoning`). mandatory is false: no spec key says a model
// refuses to be asked not to reason, and the gateway asks either way. supports_max_tokens is present when the spec's
// thinking is Anthropic's budget form, which is reasoning.max_tokens in a request.
type orReasoning struct {
	Mandatory         bool `json:"mandatory"`
	SupportsMaxTokens bool `json:"supports_max_tokens,omitempty"`
}

// orLinks is OpenRouter's links (spec `ModelLinks`): details is the model's own path in this API, the only page Axiom has
// for a model. OpenRouter points at its /models/{author}/{slug}/endpoints page, which Axiom does not serve.
type orLinks struct {
	Details string `json:"details"`
}

// newORModel is one model in OpenRouter's shape, listed as id. ok is false when the price schedule cannot fill the two
// members the shape requires of a price list: prompt and completion have no null, and a zero would say the model is
// free.
func newORModel(id string, m catalog.Model, s catalog.Serving, p catalog.Schedule) (orModel, bool, error) {
	prompt, pricedPrompt := p.BaseRate(catalog.ItemInput)
	out, pricedOutput := p.BaseRate(catalog.ItemOutput)
	if !pricedPrompt || !pricedOutput {
		return orModel{}, false, nil
	}
	d := m.Dialect()
	reasons := reasons(d, s.Abilities)
	entry := orEntry{
		ID: id, CanonicalSlug: m.Slug, Name: nameOf(m.Slug), Created: createdUnix, Pricing: newORPricing(p, s.Abilities),
		ContextLength: s.Context, Architecture: newORArchitecture(s.Abilities),
		TopProvider:         orTopProvider{ContextLength: s.Context, MaxCompletionTokens: s.MaxOutput},
		SupportedParameters: orParameters(s, d, reasons), Links: orLinks{Details: orListPath + "/" + id},
	}
	if reasons {
		entry.Reasoning = &orReasoning{SupportsMaxTokens: d.Thinking == catalog.ThinkingManual}
	}
	body, err := jsonx.MarshalCanonical(entry)
	if err != nil {
		return orModel{}, false, err
	}
	// The price orderings compare the average of the two prices; their sum orders the same way and needs no division.
	// Each is at most a thousand credits, so the sum cannot overflow.
	average, _ := prompt.Mul(1).Add(out.Mul(1)) //nolint:errcheck // cannot overflow: see above
	return orModel{
		orEntry: entry, body: body, model: m.ID, author: m.Author, prompt: prompt, output: out,
		search: strings.ToLower(m.Slug), average: average,
	}, true, nil
}

// reasons says whether the model reasons: the ability is what a request may ask for, and a spec that says the model does
// not think at all (thinking: "none") is the one claim that overrides it.
func reasons(d catalog.Dialect, a catalog.AbilitySet) bool {
	return a.Has(catalog.AbilityReasoning) && d.Thinking != catalog.ThinkingNone
}

// nameOf is the display name of a model the catalog keeps only a name for: the vendor's own, which is the part after the
// author ("claude-sonnet-4.5"). OpenRouter writes a title ("Claude Sonnet 4.5"), which Axiom has no data for and will not
// invent.
func nameOf(slug string) string {
	_, name, _ := strings.Cut(slug, "/")
	return name
}

// newORArchitecture is the modalities a request may carry. The catalog records images and documents apart from text and
// nothing else: audio and video inputs are not abilities, so they are not claimed.
func newORArchitecture(a catalog.AbilitySet) orArchitecture {
	inputs := []string{"text"}
	if a.Has(catalog.AbilityImages) {
		inputs = append(inputs, "image")
	}
	if a.Has(catalog.AbilityDocuments) {
		inputs = append(inputs, "file")
	}
	return orArchitecture{
		Modality: strings.Join(inputs, "+") + "->text", InputModalities: inputs, OutputModalities: []string{"text"},
	}
}

// newORPricing is the sale price as OpenRouter's price list. image_token and internal_reasoning are shown only for a
// model that takes images or reasons: the schedule falls back to the input or the output price for those items, so a
// text-only model would otherwise be listed with a price for something it cannot be charged for.
func newORPricing(p catalog.Schedule, a catalog.AbilitySet) orPricing {
	price := orPricing{
		Prompt:            orPrice(p, catalog.ItemInput),
		Completion:        orPrice(p, catalog.ItemOutput),
		InputCacheRead:    orPrice(p, catalog.ItemCacheRead),
		InputCacheWrite:   orPrice(p, catalog.ItemCacheWrite),
		InputCacheWrite1h: orPrice(p, catalog.ItemCacheWrite1h),
		Audio:             orPrice(p, catalog.ItemAudioIn),
		AudioOutput:       orPrice(p, catalog.ItemAudioOut),
		WebSearch:         orPrice(p, catalog.ItemWebSearch),
	}
	if a.Has(catalog.AbilityImages) {
		price.ImageToken = orPrice(p, catalog.ItemImageIn)
	}
	if a.Has(catalog.AbilityReasoning) {
		price.InternalReasoning = orPrice(p, catalog.ItemReasoning)
	}
	return price
}

// orPrice is the item's price as OpenRouter writes prices, or "" for an item the schedule does not price. Code execution
// is per call and has no member in OpenRouter's price list, so it is not shown either.
func orPrice(p catalog.Schedule, it catalog.Item) string {
	price, ok := p.BaseRate(it)
	if !ok {
		return ""
	}
	return price.String()
}

// orParameters is the parameters the model takes, in the order OpenRouter's parameter enum lists them (spec
// `Parameter`): the sampling and output controls every model here takes, then the ones its abilities claim. A parameter
// the gateway would pass through but the catalog says nothing about for this model (logprobs, seed, verbosity, …) is not
// claimed: a client reads the list to decide what it may send.
func orParameters(s catalog.Serving, d catalog.Dialect, reasons bool) []string {
	var p []string
	if d.DefaultSamplingOnly != catalog.ClaimYes { // a model that refuses other sampling takes neither
		p = append(p, "temperature", "top_p")
	}
	p = append(p, "max_tokens", "max_completion_tokens")
	if s.Abilities.Has(catalog.AbilityStructuredOutput) {
		p = append(p, "response_format")
	}
	p = append(p, "stop")
	if s.Abilities.Has(catalog.AbilityTools) {
		p = append(p, "tools", "tool_choice", "parallel_tool_calls")
	}
	if reasons {
		p = append(p, "reasoning", "reasoning_effort")
	}
	return p
}

// orRoom is what the buffer is given beyond the entries: the envelope's own members and the next page's link.
const orRoom = 256

// encode is the list as the key sees it and the query keeps it, in the canonical form writeJSON gives (members in key
// order), made in one buffer sized for the entries it holds. It counts first, so a key that sees few models or a query
// that keeps few does not pay for the catalog.
func (l *openRouterListing) encode(v visibility, q orQuery) []byte {
	if q.sort != orCatalogOrder {
		return l.encodeOrdered(v, q)
	}
	matched := 0
	size := orRoom
	for i := range l.models {
		if v.shows(l.models[i].model) && q.keeps(&l.models[i]) {
			matched++
			size += len(l.models[i].body) + 1
		}
	}
	first, last := q.page(matched)
	next := q.next(last, matched)
	buf := append(make([]byte, 0, size+len(next)), `{"data":[`...)
	seen, written := 0, 0
	for i := range l.models {
		e := &l.models[i]
		if !v.shows(e.model) || !q.keeps(e) {
			continue
		}
		if seen++; seen <= first || seen > last {
			continue
		}
		if written > 0 {
			buf = append(buf, ',')
		}
		written++
		buf = append(buf, e.body...)
	}
	return orFooter(buf, next, matched)
}

// encodeOrdered is encode with one of the orderings: the matches are gathered, ordered, and the page copied out of them.
// It allocates the matches as well as the buffer, which is why the list is served in the catalog's order unless a sort is
// asked for.
func (l *openRouterListing) encodeOrdered(v visibility, q orQuery) []byte {
	matches := make([]*orModel, 0, len(l.models))
	size := orRoom
	for i := range l.models {
		if v.shows(l.models[i].model) && q.keeps(&l.models[i]) {
			matches = append(matches, &l.models[i])
			size += len(l.models[i].body) + 1
		}
	}
	slices.SortFunc(matches, q.compare)
	first, last := q.page(len(matches))
	next := q.next(last, len(matches))
	buf := append(make([]byte, 0, size+len(next)), `{"data":[`...)
	for i, e := range matches[first:last] {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, e.body...)
	}
	return orFooter(buf, next, len(matches))
}

// orFooter writes the members after the data: the next page's link, null on the last page, and how many models the query
// matched in all.
func orFooter(buf []byte, next string, total int) []byte {
	buf = append(buf, `],"links":{"next":`...)
	if next == "" {
		buf = append(buf, "null"...)
	} else {
		buf = append(buf, '"')
		buf = append(buf, next...)
		buf = append(buf, '"')
	}
	buf = append(buf, `},"total_count":`...)
	buf = strconv.AppendInt(buf, int64(total), 10)
	return append(buf, '}')
}
