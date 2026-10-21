package gateway

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/routing"
)

// The model list in OpenRouter's shape (docs/openrouter-parity.md §2): the models GET /v1/models lists, with the price
// the site sells each at and the limits, modalities and parameters its spec declares. It is the same list, so the two
// prefixes cannot disagree about which models a key may call; only the members differ, and /v1 stays in OpenAI's and
// Anthropic's shapes for the clients that read those.

// orListPath is where the list is served and what its links point at.
const orListPath = "/api/v1/models"

// orMaxLimit is the most models one page may hold: the maximum of OpenRouter's limit parameter (OpenAPI `/models`).
const orMaxLimit = 1000

// listOpenRouterModels answers GET /api/v1/models in OpenRouter's shape: every model this key may call that the routing
// snapshot serves, with the price the site sells it at, filtered and paged by the query parameters Axiom keeps. The list
// is the one the last build published with the snapshot, so the request reads no price and no spec of its own.
func (h *Handler) listOpenRouterModels(w http.ResponseWriter, r *http.Request) {
	format := formatFor(r)
	release, ok := h.ipSlot(w, r, format)
	if !ok {
		return
	}
	defer release()
	var q orQuery
	v, err := h.visible(r)
	if err == nil {
		q, err = readORQuery(r)
	}
	if err != nil {
		h.fail(w, r, format, err)
		return
	}
	writeBody(w, h.Models.published().encode(v, q)) // no-store: the list is this key's, and its prices are the site's
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
		entry, err = h.Models.published().entry(v, r.PathValue("model"))
	}
	if err != nil {
		h.fail(w, r, format, err)
		return
	}
	writeJSON(w, orModelResponse{Data: entry})
}

// entry is the model the name resolves to, as the list holds it: the name is resolved against the catalog snapshot the
// list was built from, so the entry is the one the list shows and not one a newer snapshot would describe.
func (l *openRouterListing) entry(v visibility, name string) (orEntry, error) {
	if l.snap == nil { // before the first build: nothing is routed, so nothing is fetched
		return orEntry{}, errNoModel
	}
	resolved, err := l.snap.ResolvePublic(name)
	if err != nil {
		return orEntry{}, err
	}
	if !l.shows(v, resolved.Model) {
		return orEntry{}, errNoModel
	}
	for i := range l.models {
		if l.models[i].model == resolved.Model {
			return l.models[i].orEntry, nil
		}
	}
	return orEntry{}, errNoModel
}

// ModelList is the list in OpenRouter's shape that the handler serves: the one published with the routing snapshot it was
// built for. A build makes both from one reading of the catalog and the sale prices and publishes them together
// (RouteDeps.Publish), so the price a client reads is the price the snapshot routes at, and a build that fails publishes
// neither. Nothing else fills it, and nothing reads the database or the catalog to fill it.
type ModelList struct {
	// Publication excludes both readers and route planning until both pointers have changed.
	mu   sync.RWMutex
	last *openRouterListing
}

// NewModelList is the empty list a node's route builder fills and its handler serves: before the first build it holds no
// model, which is the truth — nothing is routed until a snapshot exists.
func NewModelList() *ModelList { return &ModelList{} }

// published is the last list a build published, or the empty one before the first.
func (m *ModelList) published() *openRouterListing {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.last != nil {
		return m.last
	}
	return &openRouterListing{}
}

// plan cannot observe the interval between the route and list stores in Publish. The lock covers memory work only,
// never admission's database transaction or a list response's encoding.
func (m *ModelList) plan(router *routing.Router, in routing.Input) (routing.Plan, *routing.Wait, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return router.Plan(in)
}

// openRouterListing is the models of one build in OpenRouter's shape, encoded once per model: what a request pays is
// keeping the entries its key may see and its query keeps, and copying the page out. Its prices are the ones the routing
// snapshot built in the same build routes at.
type openRouterListing struct {
	snap   *catalog.Snapshot
	served map[catalog.ModelID]struct{} // the models the build gave an endpoint: a call to one has a route to take
	built  time.Time                    // when the build began; the snapshot of the same build carries it too
	models []orModel                    // in the snapshot's order
}

// shows says whether the model is in this list for this key: the build had a route for it and the key may call it. The
// answer is the build's, never the router's of the moment, so a route that appears or goes while a response is written
// cannot make its total, its page and its entries disagree.
func (l *openRouterListing) shows(v visibility, m catalog.ModelID) bool {
	_, served := l.served[m]
	return served && v.allows(m)
}

// newOpenRouterListing lists every model the catalog lists that the build serves and a rule prices, one entry each. A
// model without a sale price is left out for the reason routing leaves its endpoints out, and one whose spec does not
// declare its limits or whose price cannot fill prompt and completion is left out because OpenRouter's object cannot be
// written without them.
func newOpenRouterListing(snap *catalog.Snapshot, prices map[catalog.ModelID]billing.Sold,
	served map[catalog.ModelID]struct{},
) (*openRouterListing, error) {
	l := &openRouterListing{snap: snap, served: served}
	for m := range snap.Models() {
		if _, ok := served[m.ID]; !ok {
			continue
		}
		serving, serves := m.Serving()
		sold, priced := prices[m.ID]
		if !serves || !priced {
			continue
		}
		id, ok := l.publicID(m)
		if !ok {
			continue
		}
		e, ok, err := newORModel(id, m, serving, sold.Schedule)
		if err != nil {
			return nil, err
		}
		if ok {
			l.models = append(l.models, e)
		}
	}
	return l, nil
}

// publicID is the name the model is served and fetched under: its standard ID, the slug without the internal version
// suffix ("acme/claude@20250929" is listed and called as "acme/claude"). The catalog lists a model only when that name
// resolves to it (catalog's requireListable), and a name that does not is left out of the list: a client could neither
// fetch the model by it nor call it, and the internal slug names a version the API does not accept.
func (l *openRouterListing) publicID(m catalog.Model) (string, bool) {
	id := catalog.Normalize(strings.SplitN(m.Slug, "@", 2)[0])
	r, err := l.snap.ResolvePublic(id)
	return id, err == nil && r.Model == m.ID
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

// newORModel is one model in OpenRouter's shape, listed and fetched as id, the model's public name. ok is false when the
// price schedule cannot fill the two members the shape requires of a price list: prompt and completion have no null, and
// a zero would say the model is free.
func newORModel(id string, m catalog.Model, s catalog.Serving, p catalog.Schedule) (orModel, bool, error) {
	prompt, pricedPrompt := p.BaseRate(catalog.ItemInput)
	out, pricedOutput := p.BaseRate(catalog.ItemOutput)
	if !pricedPrompt || !pricedOutput {
		return orModel{}, false, nil
	}
	d := m.Dialect()
	reasons := reasons(d, s.Abilities)
	entry := orEntry{
		ID: id, CanonicalSlug: id, Name: nameOf(id), Created: createdUnix, Pricing: newORPricing(p, s.Abilities),
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
		search: strings.ToLower(id), average: average,
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

// orRoom is what the buffer is given beyond the page's entries: the envelope's own members and the next page's link.
const orRoom = 256

// encode is the list as the key sees it and the query keeps it, in the canonical form writeJSON gives (members in key
// order), made in one buffer sized for the page it serves. It counts first, so a key that sees few models, a query that
// keeps few and a page that holds few do not pay for the catalog: the buffer grows with the page, not with the matches.
func (l *openRouterListing) encode(v visibility, q orQuery) []byte {
	if q.sort != orCatalogOrder {
		return l.encodeOrdered(v, q)
	}
	matched, size := l.pageStats(v, q)
	return l.encodePage(v, q, matched, size)
}

func (l *openRouterListing) encodePage(v visibility, q orQuery, matched, size int) []byte {
	first, last := q.page(matched)
	next := q.next(last, matched)
	buf := append(make([]byte, 0, orRoom+len(next)+size), `{"data":[`...)
	seen, written := 0, 0
	for i := range l.models {
		e := &l.models[i]
		if !l.shows(v, e.model) || !q.keeps(e) {
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
// asked for; the buffer itself is the page's, as in encode.
func (l *openRouterListing) encodeOrdered(v visibility, q orQuery) []byte {
	matches := make([]*orModel, 0, len(l.models))
	for i := range l.models {
		if l.shows(v, l.models[i].model) && q.keeps(&l.models[i]) {
			matches = append(matches, &l.models[i])
		}
	}
	slices.SortFunc(matches, q.compare)
	first, last := q.page(len(matches))
	next := q.next(last, len(matches))
	size := orRoom + len(next)
	for _, e := range matches[first:last] {
		size += len(e.body) + 1
	}
	buf := append(make([]byte, 0, size), `{"data":[`...)
	for i, e := range matches[first:last] {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, e.body...)
	}
	return orFooter(buf, next, len(matches))
}

// pageStats counts all matches but sizes only the page; subtraction avoids adding an unbounded offset to the limit.
func (l *openRouterListing) pageStats(v visibility, q orQuery) (matched, size int) {
	for i := range l.models {
		e := &l.models[i]
		if !l.shows(v, e.model) || !q.keeps(e) {
			continue
		}
		matched++
		if matched > q.offset && (q.limit == 0 || matched-q.offset <= q.limit) {
			size += len(e.body) + 1
		}
	}
	return matched, size
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
