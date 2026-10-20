package gateway

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/alloctest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/routing"
)

// listedOR is a model of the list in OpenRouter's shape, as the tests read it: the members Axiom fills, and the required
// ones it has no data for as raw JSON, so that null and absent are told apart.
type listedOR struct {
	ID            string            `json:"id"`
	CanonicalSlug string            `json:"canonical_slug"`
	Name          string            `json:"name"`
	Created       *int64            `json:"created"`
	Pricing       map[string]string `json:"pricing"`
	ContextLength int64             `json:"context_length"`
	Architecture  struct {
		Modality         string   `json:"modality"`
		InputModalities  []string `json:"input_modalities"`
		OutputModalities []string `json:"output_modalities"`
	} `json:"architecture"`
	TopProvider struct {
		ContextLength       int64 `json:"context_length"`
		MaxCompletionTokens int64 `json:"max_completion_tokens"`
		IsModerated         *bool `json:"is_moderated"`
	} `json:"top_provider"`
	PerRequestLimits    jsonx.Raw `json:"per_request_limits"`
	SupportedParameters []string  `json:"supported_parameters"`
	DefaultParameters   jsonx.Raw `json:"default_parameters"`
	SupportedVoices     jsonx.Raw `json:"supported_voices"`
	Reasoning           jsonx.Raw `json:"reasoning"`
	Links               struct {
		Details string `json:"details"`
	} `json:"links"`
}

// complete reports whether the model carries every member OpenRouter's object requires of the models Axiom lists, with the
// three it has no data for present as null rather than left out.
func (m listedOR) complete() bool {
	return m.ID != "" && m.CanonicalSlug != "" && m.Name != "" && m.Created != nil && m.priced() && m.limited() && m.modal() &&
		m.SupportedParameters != nil && m.Links.Details != "" &&
		string(m.PerRequestLimits) == "null" && string(m.DefaultParameters) == "null" && string(m.SupportedVoices) == "null"
}

// priced reports whether a price the shape requires is there.
func (m listedOR) priced() bool {
	return m.Pricing["prompt"] != "" && m.Pricing["completion"] != ""
}

// limited reports whether the model's limits are the shape's, unmoderated as the gateway's models are.
func (m listedOR) limited() bool {
	return m.ContextLength > 0 && m.TopProvider.IsModerated != nil && !*m.TopProvider.IsModerated &&
		m.TopProvider.ContextLength > 0 && m.TopProvider.MaxCompletionTokens > 0
}

// modal reports whether the modalities are a modality Axiom's models answer.
func (m listedOR) modal() bool {
	return m.Architecture.Modality != "" && len(m.Architecture.InputModalities) > 0 &&
		slices.Equal(m.Architecture.OutputModalities, []string{"text"})
}

// orResponse is OpenRouter's list (spec `ModelsListResponse`): the page, the link to the next one and how many models the
// query matched in all.
type orResponse struct {
	Data  []listedOR `json:"data"`
	Links struct {
		Next *string `json:"next"`
	} `json:"links"`
	TotalCount int `json:"total_count"`
}

// orList reads the list in OpenRouter's shape at path and checks its envelope: every model carries the members the shape
// requires, and the total is the whole match, not the page.
func (r *rig) orList(a account, path string) orResponse {
	r.t.Helper()
	resp, body := r.do(http.MethodGet, path, a.key, nil)
	var list orResponse
	if resp.StatusCode != http.StatusOK || jsonx.Unmarshal([]byte(body), &list) != nil || list.TotalCount < len(list.Data) {
		r.t.Fatalf("%d %s", resp.StatusCode, body)
	}
	for _, m := range list.Data {
		if !m.complete() {
			r.t.Errorf("model %+v lacks a member OpenRouter's shape requires", m)
		}
	}
	return list
}

// orIDs reads the list and returns its models' ids, in the order it serves them.
func (r *rig) orIDs(a account) []string {
	r.t.Helper()
	list := r.orList(a, orListPath)
	ids := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		ids = append(ids, m.ID)
	}
	return ids
}

// orModel is the one model of the list with this id, and the query's page it is on.
func (r *rig) orModel(a account, id string) listedOR {
	r.t.Helper()
	for _, m := range r.orList(a, orListPath).Data {
		if m.ID == id {
			return m
		}
	}
	r.t.Fatalf("%s does not list %s", orListPath, id)
	return listedOR{}
}

// orEntry is the model's object exactly as it is served, for a comparison of the whole of it.
func (r *rig) orEntry(a account, id string) string {
	r.t.Helper()
	resp, body := r.do(http.MethodGet, orListPath, a.key, nil)
	var list struct {
		Data []jsonx.Raw `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || jsonx.Unmarshal([]byte(body), &list) != nil {
		r.t.Fatalf("%d %s", resp.StatusCode, body)
	}
	for _, raw := range list.Data {
		var head struct {
			ID string `json:"id"`
		}
		if err := jsonx.Unmarshal(raw, &head); err != nil {
			r.t.Fatalf("%v: %s", err, raw)
		}
		if head.ID == id {
			return string(raw)
		}
	}
	r.t.Fatalf("%s does not list %s", orListPath, id)
	return ""
}

// decode is a JSON document as a value, for a comparison that does not depend on the order of the members.
func decode(t *testing.T, body string) any {
	t.Helper()
	var v any
	if err := jsonx.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return v
}

const (
	// goldenSpec is a model with every ability the shape shows and a manual thinking budget, so that the parameters and
	// the reasoning metadata it lists are the ones its spec claims.
	goldenSpec = `{"items":["input","output","cache_read","cache_write","cache_write_1h","reasoning","image_in",` +
		`"audio_in","audio_out","web_search"],"tiers":[],"context_length":200000,"max_output_tokens":64000,"streams":true,` +
		`"abilities":["tools","images","documents","reasoning","prompt_cache","structured_output"],"thinking":"manual"}`

	// goldenPrice prices every item OpenRouter's price list has a member for, under a context tier above 200000: the list
	// shows the standard tier's price below the first threshold, which is the price the shape means by a model's price.
	goldenPrice = `{"v":1,"currency":"USD","rates":{"input":"0.000003","output":"0.000015","cache_read":"0.0000003",` +
		`"cache_write":"0.00000375","cache_write_1h":"0.000006","reasoning":"0.00002","image_in":"0.000004",` +
		`"audio_in":"0.00000001","audio_out":"0.00000002","web_search":"0.01"},` +
		`"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":200000,"rates":{"input":"0.000006",` +
		`"output":"0.0000225"}}]}}`

	// golden is OpenRouter's object for goldenSpec at goldenPrice, member for member: prices as the exact strings of the
	// schedule, the limits and modalities the spec declares, and the parameters and reasoning metadata the abilities claim.
	golden = `{
		"architecture": {"input_modalities": ["text", "image", "file"], "modality": "text+image+file->text",
			"output_modalities": ["text"]},
		"canonical_slug": "acme/golden",
		"context_length": 200000,
		"created": 0,
		"default_parameters": null,
		"id": "acme/golden",
		"links": {"details": "/api/v1/models/acme/golden"},
		"name": "golden",
		"per_request_limits": null,
		"pricing": {"audio": "0.00000001", "audio_output": "0.00000002", "completion": "0.000015",
			"image_token": "0.000004", "input_cache_read": "0.0000003", "input_cache_write": "0.00000375",
			"input_cache_write_1h": "0.000006", "internal_reasoning": "0.00002", "prompt": "0.000003",
			"web_search": "0.01"},
		"reasoning": {"mandatory": false, "supports_max_tokens": true},
		"supported_parameters": ["temperature", "top_p", "max_tokens", "max_completion_tokens", "response_format", "stop",
			"tools", "tool_choice", "parallel_tool_calls", "reasoning", "reasoning_effort"],
		"supported_voices": null,
		"top_provider": {"context_length": 200000, "is_moderated": false, "max_completion_tokens": 64000}
	}`
)

// goldenRig is the seeded rig with acme/golden in it, served by an endpoint.
func goldenRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	id := r.listed("acme/golden", goldenSpec, goldenPrice)
	if err := r.catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.attachEndpoint(id)
	return r
}

// TestOpenRouterModelShape: GET /api/v1/models serves the models OpenRouter's object describes, member for member, from
// the sale price and the spec. A model with cache, reasoning, image, audio and search prices gets the ten members Axiom
// can fill with their exact price strings, and a plain one gets prompt, completion and the cache write it prices, with
// the members it has no data for absent rather than zero.
func TestOpenRouterModelShape(t *testing.T) {
	t.Parallel()
	r := goldenRig(t)
	a := r.newAccount(1e9)
	list := r.orList(a, "/api/v1/models")
	if list.TotalCount != 4 || len(list.Data) != 4 || list.Links.Next != nil {
		t.Errorf("%d models, total %d, next %v; want the four of the catalog", len(list.Data), list.TotalCount, list.Links.Next)
	}
	if diff := cmp.Diff(decode(t, golden), decode(t, r.orEntry(a, "acme/golden"))); diff != "" {
		t.Errorf("acme/golden (-want +got):\n%s", diff)
	}
	want := []string{"acme/alpha", "acme/beta", "anthropic/claude-live", "acme/golden"}
	if diff := cmp.Diff(want, r.orIDs(a)); diff != "" {
		t.Errorf("the catalog's order (-want +got):\n%s", diff)
	}
	// The seeded model prices the prompt, the completion and a cache write, and nothing else.
	alpha := r.orModel(a, "acme/alpha")
	prices := map[string]string{"prompt": "0.000001", "completion": "0.000002", "input_cache_write": "0.000001"}
	if diff := cmp.Diff(prices, alpha.Pricing); diff != "" {
		t.Errorf("a plain model's prices (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"text"}, alpha.Architecture.InputModalities); diff != "" {
		t.Errorf("a text-only model's inputs (-want +got):\n%s", diff)
	}
	if alpha.Architecture.Modality != "text->text" || len(alpha.Reasoning) != 0 {
		t.Errorf("a text-only model: modality %q, reasoning %s", alpha.Architecture.Modality, alpha.Reasoning)
	}
	parameters := []string{"temperature", "top_p", "max_tokens", "max_completion_tokens", "stop"}
	if diff := cmp.Diff(parameters, alpha.SupportedParameters); diff != "" {
		t.Errorf("a text-only model's parameters (-want +got):\n%s", diff)
	}
}

// TestOpenRouterListFilters: the query parameters Axiom keeps filter and order the list as OpenRouter's say, the page's
// link is the request's own query with the offset moved, and a name OpenRouter does not document is ignored, so a tracker
// in the URL is not an error.
func TestOpenRouterListFilters(t *testing.T) {
	t.Parallel()
	r := goldenRig(t)
	a := r.newAccount(1e9)
	all := []string{"acme/alpha", "acme/beta", "anthropic/claude-live", "acme/golden"}
	for name, tc := range map[string]struct {
		query string
		ids   []string
		next  string // the whole link to the next page; none when empty
		total int    // 0: as many as ids
	}{
		"a page":                    {"?limit=2", []string{"acme/alpha", "acme/beta"}, "/api/v1/models?limit=2&offset=2", 4},
		"the page after it":         {"?limit=2&offset=2", []string{"anthropic/claude-live", "acme/golden"}, "", 4},
		"an offset past the end":    {"?offset=9", nil, "", 4},
		"a search on the name":      {"?q=GOLD", []string{"acme/golden"}, "", 1},
		"a search on the author":    {"?q=acme", []string{"acme/alpha", "acme/beta", "acme/golden"}, "", 3},
		"the context at least":      {"?context=150000", []string{"acme/golden"}, "", 1},
		"the prompt price at least": {"?min_price=2", []string{"acme/golden"}, "", 1},
		"the prompt price at most":  {"?max_price=1", []string{"acme/alpha", "acme/beta", "anthropic/claude-live"}, "", 3},
		"the output price at least": {"?min_output_price=10", []string{"acme/golden"}, "", 1},
		"the output price at most":  {"?max_output_price=2", []string{"acme/alpha", "acme/beta", "anthropic/claude-live"}, "", 3},
		"an input modality":         {"?input_modalities=image", []string{"acme/golden"}, "", 1},
		"every input modality":      {"?input_modalities=text,file", []string{"acme/golden"}, "", 1},
		"an output modality":        {"?output_modalities=image", nil, "", 0},
		"an output set":             {"?output_modalities=text,image", all, "", 4},
		"every output":              {"?output_modalities=all", all, "", 4},
		"a parameter":               {"?supported_parameters=tools", []string{"acme/golden"}, "", 1},
		"an author":                 {"?model_authors=anthropic", []string{"anthropic/claude-live"}, "", 1},
		"two authors":               {"?model_authors=acme,anthropic", all, "", 4},
		"cheapest first":            {"?sort=pricing-low-to-high", all, "", 4},
		"dearest first":             {"?sort=pricing-high-to-low", []string{"acme/golden", "acme/alpha", "acme/beta", "anthropic/claude-live"}, "", 4},
		"longest context first":     {"?sort=context-high-to-low", []string{"acme/golden", "acme/alpha", "acme/beta", "anthropic/claude-live"}, "", 4},
		"a name nobody documents":   {"?utm_source=twitter", all, "", 4},
		"a filter and a sort":       {"?input_modalities=image&sort=pricing-low-to-high", []string{"acme/golden"}, "", 1},
	} {
		t.Run(name, func(t *testing.T) { checkORPage(t, r, a, tc.query, tc.ids, tc.next, tc.total) })
	}
}

// checkORPage: the list at the query serves exactly ids, in that order, over a total of total matches (0: as many as
// ids), and its link to the next page is next.
func checkORPage(t *testing.T, r *rig, a account, query string, ids []string, next string, total int) {
	t.Helper()
	path := "/api/v1/models" + query
	list := r.orList(a, path)
	if diff := cmp.Diff(append([]string{}, ids...), idsOf(list)); diff != "" {
		t.Errorf("%s (-want +got):\n%s", path, diff)
	}
	if total == 0 {
		total = len(ids)
	}
	if list.TotalCount != total || len(list.Data) != len(ids) {
		t.Errorf("%s: %d models, total %d; want %d of %d", path, len(list.Data), list.TotalCount, len(ids), total)
	}
	var link string
	if list.Links.Next != nil {
		link = *list.Links.Next
	}
	if link != next {
		t.Errorf("%s: next %q, want %q", path, link, next)
	}
}

// TestOpenRouterListRefusals: a query parameter OpenRouter documents that Axiom has no data for, and a value one it reads
// does not accept, are refused by name rather than silently ignored: a filter the server dropped is an answer the client
// cannot see is wrong.
func TestOpenRouterListRefusals(t *testing.T) {
	t.Parallel()
	r := goldenRig(t)
	a := r.newAccount(1e9)
	for name, tc := range map[string]struct{ query, parameter string }{
		"a category":               {"?category=roleplay", "category"},
		"an RSS feed":              {"?use_rss=1", "use_rss"},
		"a sort with no data":      {"?sort=most-popular", "sort"},
		"a limit of 0":             {"?limit=0", "limit"},
		"a limit over the maximum": {"?limit=1001", "limit"},
		"a negative offset":        {"?offset=-1", "offset"},
		"a context of 0":           {"?context=0", "context"},
		"a price that is not one":  {"?max_price=cheap", "max_price"},
		"a price too precise":      {"?min_price=0.0000000001", "min_price"},
		"an unknown modality":      {"?input_modalities=telepathy", "input_modalities"},
		"an unknown parameter":     {"?supported_parameters=quantum", "supported_parameters"},
		"a parameter twice":        {"?limit=2&limit=3", "limit"},
	} {
		t.Run(name, func(t *testing.T) {
			resp, body := r.do(http.MethodGet, "/api/v1/models"+tc.query, a.key, nil)
			if code, typ := errorOf(t, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_request" ||
				typ != "invalid_request_error" || !strings.Contains(body, tc.parameter) {
				t.Errorf("%s: %d %s %s, want 400 invalid_request naming %q:\n%s", tc.query, resp.StatusCode, code, typ, tc.parameter, body)
			}
		})
	}
}

// TestOpenRouterQueryNotParsed: a query string the standard library cannot decode is refused, not half-read. url.Values
// drops an undecodable pair and everything after a ';' without reporting it, so the filter the client wrote would vanish
// and the answer would be the whole list.
func TestOpenRouterQueryNotParsed(t *testing.T) {
	t.Parallel()
	r := goldenRig(t)
	a := r.newAccount(1e9)
	for name, query := range map[string]string{
		"an escape that is not one": "?max_price=1%ZZ",
		"a semicolon separator":     "?max_price=1;limit=1",
	} {
		t.Run(name, func(t *testing.T) {
			resp, body := r.do(http.MethodGet, "/api/v1/models"+query, a.key, nil)
			if code, typ := errorOf(t, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_request" ||
				typ != "invalid_request_error" {
				t.Errorf("%s: %d %s %s, want 400 invalid_request:\n%s", query, resp.StatusCode, code, typ, body)
			}
		})
	}
}

// idsOf is the ids of a list, in the order it serves them.
func idsOf(list orResponse) []string {
	ids := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		ids = append(ids, m.ID)
	}
	return ids
}

// TestOpenRouterRetrieve: GET /api/v1/models/{model} is the list's object for the model the name resolves to, wrapped in
// data as OpenRouter wraps it, under the model's own id whichever of its names was asked for.
func TestOpenRouterRetrieve(t *testing.T) {
	t.Parallel()
	r := goldenRig(t)
	a := r.newAccount(1e9)
	listed := r.orEntry(a, "acme/golden")
	resp, body := r.do(http.MethodGet, "/api/v1/models/acme/golden", a.key, nil)
	var one struct {
		Data jsonx.Raw `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || jsonx.Unmarshal([]byte(body), &one) != nil {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if diff := cmp.Diff(decode(t, listed), decode(t, string(one.Data))); diff != "" {
		t.Errorf("the one model against the list's entry (-want +got):\n%s", diff)
	}
	// The maker's own name resolves to the model, whose id is the catalog's: OpenRouter's shape has one id per model.
	if m := r.orModel(a, "anthropic/claude-live"); m.Name != "claude-live" || m.CanonicalSlug != "anthropic/claude-live" {
		t.Errorf("claude-live: %+v", m)
	}
	resp, body = r.do(http.MethodGet, "/api/v1/models/claude-live", a.key, nil)
	if resp.StatusCode != http.StatusOK || jsonx.Unmarshal([]byte(body), &one) != nil || !strings.Contains(string(one.Data), `"anthropic/claude-live"`) {
		t.Errorf("the maker's name alone: %d %s", resp.StatusCode, body)
	}
}

// TestOpenRouterRetrieveMissing: a model the key may not use, one nothing serves and a name the catalog does not know are
// the same 404 the list leaves them out with, never 403.
func TestOpenRouterRetrieveMissing(t *testing.T) {
	t.Parallel()
	r := goldenRig(t)
	a, restricted := r.newAccount(1e9), r.newAccount(1e9)
	restrict(r, restricted, "{acme/alpha}")
	for _, tc := range []struct {
		name, path string
		a          account
	}{
		{"a model the key may not call", "/api/v1/models/acme/golden", restricted},
		{"a model that does not exist", "/api/v1/models/acme/none", a},
		{"a name the catalog does not know", "/api/v1/models/golden", a},
		{"a model with no endpoint", "/api/v1/models/anthropic/claude-test", a},
	} {
		resp, body := r.do(http.MethodGet, tc.path, tc.a.key, nil)
		if code, typ := errorOf(t, body); resp.StatusCode != http.StatusNotFound || code != "model_not_found" || typ != "not_found_error" {
			t.Errorf("%s: %d %s %s, want 404 model_not_found", tc.name, resp.StatusCode, code, typ)
		}
	}
}

// TestOpenRouterListIsTheBuilds: the list a client reads is the last one a build published, never a price read at request
// time. A sale price that changes without a build is not what a request shows, because the list has no reader of its own; a
// price that changes with a build is. A model listed after the snapshot changed is in the list of the build that serves it.
func TestOpenRouterListIsTheBuilds(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	if got := r.orModel(a, "acme/alpha").Pricing["prompt"]; got != "0.000001" {
		t.Fatalf("the price at first: %q", got)
	}
	// A sale price that changes and is not built is not what the request shows: the list has no reader of its own, so a
	// request cannot quote a price the routing snapshot was not built with.
	next, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.000007","output":"0.000009"}}`))
	if err != nil {
		t.Fatal(err)
	}
	r.sold[r.alpha] = billing.Sold{Version: uuid.NewV7(), Schedule: next}
	if got := r.orModel(a, "acme/alpha").Pricing["prompt"]; got != "0.000001" {
		t.Errorf("the price a request read without a build: %q, want the one the list was built with", got)
	}
	// The next build publishes it, and the list follows the build.
	r.publish()
	if got := r.orModel(a, "acme/alpha").Pricing["prompt"]; got != "0.000007" {
		t.Errorf("the price after the build: %q, want the new one", got)
	}
	// A model listed after the snapshot changed is in the list of the build that serves it, and not before it: the request
	// reads no catalog and no price of its own.
	late := r.listed("acme/late", basicSpec, cheap)
	if err := r.catalog.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(r.orIDs(a), "acme/late") {
		t.Errorf("a model the catalog lists but no build serves is in the list: %v", r.orIDs(a))
	}
	r.attachEndpoint(late)
	if !slices.Contains(r.orIDs(a), "acme/late") {
		t.Errorf("a model listed after the snapshot changed is not in the list: %v", r.orIDs(a))
	}
}

// TestOpenRouterListIsOneBuild: the total, the page and the entries of one response come from the one build the request
// read, so a route the router loses in between cannot make the total say two while the page holds one and its link says
// there is no more.
func TestOpenRouterListIsOneBuild(t *testing.T) {
	t.Parallel()
	r := goldenRig(t)
	v := visibility{} // every model, and no router: the list answers for the build, as it does in a request
	l := r.models.published()
	q := orQuery{limit: 2}
	both := l.encode(v, q)
	counted, resume := make(chan struct{}), make(chan struct{})
	encoded := make(chan []byte, 1)
	go func() {
		matched, size := l.pageStats(v, q)
		close(counted)
		<-resume
		encoded <- l.encodePage(v, q, matched, size)
	}()
	<-counted
	// The route of the model on the second place goes, and its entry stays in the page the build made, with the total and
	// the link that build's count gives.
	var rest []routing.Endpoint
	for name, e := range r.endpoints {
		if name != "beta-1" {
			rest = append(rest, e)
		}
	}
	r.install(routing.Preset{Classes: routing.AllClasses, Policies: routing.AllPolicies, Strategy: routing.Cheapest}, rest...)
	close(resume)
	if after := <-encoded; string(after) != string(both) {
		t.Errorf("one response of one build changed when the router did:\nbefore %s\nafter  %s", both, after)
	}
	// A page of one over that build still says how many it matched and links to the next, which is what the entries agree
	// with only if the count and the page are the same build's.
	var page orResponse
	if err := jsonx.Unmarshal(l.encode(v, orQuery{limit: 1}), &page); err != nil {
		t.Fatal(err)
	}
	if page.TotalCount != 4 || len(page.Data) != 1 || page.Links.Next == nil {
		t.Errorf("%d models, total %d, next %v; want one of four with a link", len(page.Data), page.TotalCount, page.Links.Next)
	}
}

// TestOpenRouterPageBuffer: the buffer a page is encoded in is the page's, not the catalog's. A page of one model out of a
// thousand, and a page past the end, cost what they hold; the matches a sorted page sorts are the one cost that grows with
// the catalog, which is why the catalog's order is the list's own. Measurements run in an isolated test process.
func TestOpenRouterPageBuffer(t *testing.T) {
	const models = 1000
	l := orListing(t, models)
	cases := []struct {
		name string
		q    orQuery
		want float64 // the most one encoding may allocate
	}{
		{"one", orQuery{limit: 1}, 4 << 10},
		{"empty", orQuery{offset: models + 10}, 1 << 10},
		{"sorted-one", orQuery{limit: 1, sort: orCheapestFirst}, 16 << 10},
		{"sorted-empty", orQuery{offset: models + 10, sort: orCheapestFirst}, 12 << 10},
	}
	funcs := make([]alloctest.Function, 0, len(cases))
	for _, tc := range cases {
		funcs = append(funcs, alloctest.Function{Name: tc.name, F: func() { sink = l.encode(visibility{}, tc.q) }})
	}
	for name, got := range alloctest.Measure(t, funcs...) {
		for _, tc := range cases {
			if name == tc.name && got.Bytes > tc.want {
				t.Errorf("%s allocates %.0f bytes, want at most %.0f", name, got.Bytes, tc.want)
			}
		}
		t.Logf("%s: %.0f bytes, %.0f allocations", name, got.Bytes, got.Count)
	}
}

// sink keeps the encoding of the page's buffer test from being optimized away.
var sink []byte

// TestOpenRouterEmptyList: a key that may call no model gets OpenRouter's empty list, with no next page and a total of
// zero, encoded in the canonical order of its members.
func TestOpenRouterEmptyList(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	none := r.newAccount(1e9)
	restrict(r, none, "{}")
	resp, body := r.do(http.MethodGet, "/api/v1/models", none.key, nil)
	if want := `{"data":[],"links":{"next":null},"total_count":0}`; resp.StatusCode != http.StatusOK || body != want {
		t.Errorf("%d %q, want 200 %q", resp.StatusCode, body, want)
	}
}

// orDocumented are the query parameters of OpenRouter's GET /api/v1/models (OpenAPI, read 2026-10-19).
var orDocumented = []string{
	"offset", "limit", "category", "supported_parameters", "output_modalities", "sort", "use_rss", "use_rss_chat_links",
	"q", "input_modalities", "context", "min_price", "max_price", "arch", "model_authors", "providers", "distillable",
	"zdr", "region", "min_output_price", "max_output_price", "min_age_days", "max_age_days", "min_intelligence_index",
	"max_intelligence_index", "min_coding_index", "max_coding_index", "min_agentic_index", "max_agentic_index",
	"min_tool_success_rate", "max_tool_success_rate",
}

// TestOpenRouterQueryParameters: every parameter OpenRouter documents is either read or refused, never both and never
// neither, and neither map holds a name it does not document: a filter the server drops is an answer the client cannot
// trust.
func TestOpenRouterQueryParameters(t *testing.T) {
	t.Parallel()
	documented := make(map[string]struct{}, len(orDocumented))
	for _, name := range orDocumented {
		documented[name] = struct{}{}
		_, read := orQueryMembers[name]
		_, refused := orRefused[name]
		if read == refused {
			t.Errorf("%q is read %v and refused %v, want exactly one", name, read, refused)
		}
	}
	for name := range orQueryMembers {
		if _, ok := documented[name]; !ok {
			t.Errorf("%q is read and is not a parameter of OpenRouter's list", name)
		}
	}
	for name := range orRefused {
		if _, ok := documented[name]; !ok {
			t.Errorf("%q is refused and is not a parameter of OpenRouter's list", name)
		}
	}
}
