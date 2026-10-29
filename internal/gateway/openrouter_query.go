package gateway

import (
	"cmp"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"axiom/internal/platform"
)

// orSort is the order the list is served in.
type orSort uint8

// The orderings. The catalog's own comes first and is the one the list is served in unless a sort is asked for.
const (
	orCatalogOrder orSort = iota
	orCheapestFirst
	orDearestFirst
	orLongestContext
)

// orOrderings are the values of sort Axiom's data can rank by: the average of a model's prompt and completion prices, and
// its context length.
var orOrderings = map[string]orSort{
	"pricing-low-to-high": orCheapestFirst, "pricing-high-to-low": orDearestFirst, "context-high-to-low": orLongestContext,
}

// orUnranked are the values of sort that are refused, with the data ranking by them would need.
var orUnranked = map[string]string{
	"most-popular":                 "Axiom counts no requests per model",
	"top-weekly":                   "Axiom counts no tokens per model",
	"newest":                       "the catalog keeps no creation time",
	"throughput-high-to-low":       "Axiom measures no throughput for the list",
	"latency-low-to-high":          "Axiom measures no latency for the list",
	"intelligence-high-to-low":     "Axiom has no benchmark scores",
	"coding-high-to-low":           "Axiom has no benchmark scores",
	"agentic-high-to-low":          "Axiom has no benchmark scores",
	"design-arena-elo-high-to-low": "Axiom has no benchmark scores",
}

// orQuery is what the list's query parameters ask for: the filters and the ordering Axiom has the data for. The zero
// value keeps every model, in the catalog's order.
type orQuery struct {
	path    string     // the list or user-list path for next-page links
	values  url.Values // the request's own query, which the next page's link repeats
	offset  int
	limit   int    // 0: every match, as OpenRouter returns when neither offset nor limit is given
	search  string // folded, for q
	context int64
	prompt  orRange // the price of a prompt token, in dollars per million tokens
	output  orRange // the price of a completion token, likewise
	inputs  []string
	outputs []string
	params  []string
	authors []string
	sort    orSort
}

// orRange is a bound on a price, a nil one being a bound the request does not set.
type orRange struct{ min, max *platform.UnitPrice }

// keeps reports whether p is within the range.
func (r orRange) keeps(p platform.UnitPrice) bool {
	return (r.min == nil || p.Cmp(*r.min) >= 0) && (r.max == nil || p.Cmp(*r.max) <= 0)
}

// orQueryMembers read the query parameters Axiom keeps, each with the refusal that says what it wanted. A parameter is in
// exactly one of orQueryMembers and orRefused (TestOpenRouterQueryParameters holds both to OpenRouter's documented set).
var orQueryMembers = map[string]func(q *orQuery, name, value string) error{
	"offset":               readOffset,
	"limit":                readLimit,
	"q":                    readSearch,
	"context":              readContext,
	"min_price":            (*orQuery).readPrice,
	"max_price":            (*orQuery).readPrice,
	"min_output_price":     (*orQuery).readPrice,
	"max_output_price":     (*orQuery).readPrice,
	"input_modalities":     readInputModalities,
	"output_modalities":    readOutputModalities,
	"supported_parameters": readSupportedParameters,
	"model_authors":        readAuthors,
	"sort":                 (*orQuery).readSort,
}

// orRefused are the query parameters OpenRouter documents that Axiom has no data to keep, and what is missing. They are
// refused by name, like the members of the provider object: a list filtered on something the server dropped is a wrong
// answer the client cannot see.
var orRefused = map[string]string{
	"category":               "the catalog has no use-case categories",
	"arch":                   "the catalog records no model family apart from the author",
	"providers":              "suppliers are not named to clients",
	"distillable":            "the catalog says nothing about distillation",
	"zdr":                    "the data policies of a model's endpoints are not in the list",
	"region":                 "suppliers are not placed in a region",
	"min_age_days":           "the catalog keeps no creation time",
	"max_age_days":           "the catalog keeps no creation time",
	"min_intelligence_index": "Axiom measures no benchmark indices",
	"max_intelligence_index": "Axiom measures no benchmark indices",
	"min_coding_index":       "Axiom measures no benchmark indices",
	"max_coding_index":       "Axiom measures no benchmark indices",
	"min_agentic_index":      "Axiom measures no benchmark indices",
	"max_agentic_index":      "Axiom measures no benchmark indices",
	"min_tool_success_rate":  "Axiom measures no tool-call success rates",
	"max_tool_success_rate":  "Axiom measures no tool-call success rates",
	"use_rss":                "the list is JSON, not an RSS feed",
	"use_rss_chat_links":     "the list is JSON, not an RSS feed",
}

// readORQuery reads the list's query parameters. A query string the standard library cannot decode is refused rather than
// half-read: url.Values drops an undecodable pair and a pair after a ';', so a filter written that way would vanish and the
// answer would be the whole list. A parameter OpenRouter documents that Axiom cannot keep is refused by name, never
// ignored. A name OpenRouter does not document is ignored: a cache buster or a tracker in the URL must not turn a list
// request into an error.
func readORQuery(r *http.Request) (orQuery, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return orQuery{}, invalid("The query string is not valid: " + err.Error())
	}
	q := orQuery{values: values, path: r.URL.Path}
	for _, name := range slices.Sorted(maps.Keys(q.values)) { // in a fixed order, so a request with two faults is told the same one
		value, written, err := orValue(name, q.values)
		if err != nil {
			return orQuery{}, err
		}
		if !written {
			continue
		}
		read, known := orQueryMembers[name]
		if !known {
			if reason, refused := orRefused[name]; refused {
				return orQuery{}, queryUnsupported(name, reason)
			}
			continue
		}
		if err := read(&q, name, value); err != nil {
			return orQuery{}, err
		}
	}
	if values.Get("offset") != "" && q.limit == 0 {
		q.limit = orDefaultLimit
	}
	return q, nil
}

// orValue is the value of one query parameter: written once and not empty. An empty parameter is one not written, as
// null is in the provider object; one written twice is refused, since the list would silently keep only one of them.
func orValue(name string, values url.Values) (value string, written bool, err error) {
	all := values[name]
	if len(all) > 1 {
		return "", false, queryMismatch(name, "given once")
	}
	if len(all) == 0 || all[0] == "" {
		return "", false, nil
	}
	return all[0], true, nil
}

func readOffset(q *orQuery, name, value string) error {
	v, err := strconv.Atoi(value)
	if err != nil || v < 0 {
		return queryMismatch(name, "an integer of at least 0")
	}
	q.offset = v
	return nil
}

func readLimit(q *orQuery, name, value string) error {
	v, err := strconv.Atoi(value)
	if err != nil || v < 1 || v > orMaxLimit {
		return queryMismatch(name, "an integer from 1 to "+strconv.Itoa(orMaxLimit))
	}
	q.limit = v
	return nil
}

func readSearch(q *orQuery, _ string, value string) error {
	q.search = strings.ToLower(value)
	return nil
}

func readContext(q *orQuery, name, value string) error {
	v, err := strconv.ParseInt(value, 10, 64)
	if err != nil || v < 1 {
		return queryMismatch(name, "an integer of at least 1")
	}
	q.context = v
	return nil
}

// readPrice reads one of the four price bounds, in dollars per million tokens: the main site's currency is the dollar, so
// perToken, which reads credits per million tokens, reads the number exactly as written, refusing one that is not a whole
// number of price units rather than rounding it. A bound beyond what a unit price holds is refused too: the client is
// told rather than the server guessing what it meant.
func (q *orQuery) readPrice(name, value string) error {
	p, ok := perToken([]byte(value))
	if !ok {
		return queryMismatch(name, "a number of dollars per million tokens with at most 9 decimals, from 0 to 1000000000")
	}
	switch name {
	case "min_price":
		q.prompt.min = &p
	case "max_price":
		q.prompt.max = &p
	case "min_output_price":
		q.output.min = &p
	case "max_output_price":
		q.output.max = &p
	}
	return nil
}

func readInputModalities(q *orQuery, name, value string) error {
	modalities, err := orList(name, value, orInputModalities)
	q.inputs = modalities
	return err
}

func readOutputModalities(q *orQuery, name, value string) error {
	modalities, err := orList(name, value, orOutputModalities)
	q.outputs = modalities
	return err
}

func readSupportedParameters(q *orQuery, name, value string) error {
	params, err := orList(name, value, orParameterNames)
	q.params = params
	return err
}

// readAuthors filters by author: the values are the catalog's own names, so there is nothing to validate.
func readAuthors(q *orQuery, _ string, value string) error {
	q.authors = strings.Split(value, ",")
	return nil
}

// readSort reads the ordering: one Axiom can rank by, or a refusal naming the data it would need.
func (q *orQuery) readSort(name, value string) error {
	if s, ok := orOrderings[value]; ok {
		q.sort = s
		return nil
	}
	if reason, ok := orUnranked[value]; ok {
		return invalid("The sort value " + value + " is not supported yet: " + reason + ".")
	}
	return queryMismatch(name, "one of "+orOneOf(orOrderings))
}

// orList reads a comma-separated list of names from a set, refusing one that is not in it and dropping repeats: an
// unknown value would otherwise keep nothing or everything, silently.
func orList(name, value string, known map[string]struct{}) ([]string, error) {
	var out []string
	for item := range strings.SplitSeq(value, ",") {
		if _, ok := known[item]; !ok {
			return nil, queryMismatch(name, "a comma-separated list of "+orOneOf(known))
		}
		if !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	return out, nil
}

// orOneOf is the names of a set, in order, for a refusal to say what it accepts.
func orOneOf[T any](set map[string]T) string {
	return strings.Join(slices.Sorted(maps.Keys(set)), ", ")
}

// The modalities and parameters OpenRouter's filters name (OpenAPI `/models`, spec `InputModality`, `Parameter`). Axiom's
// models take text and, by their abilities, images and files, and answer text: the other modalities are accepted and keep
// nothing, which is the truth about them.
var (
	orInputModalities  = map[string]struct{}{"text": {}, "image": {}, "audio": {}, "file": {}, "video": {}}
	orOutputModalities = map[string]struct{}{
		"text": {}, "image": {}, "embeddings": {}, "audio": {}, "video": {}, "rerank": {}, "decisions": {},
		"speech": {}, "transcription": {}, "all": {},
	}
	orParameterNames = map[string]struct{}{
		"temperature": {}, "top_p": {}, "top_k": {}, "min_p": {}, "top_a": {}, "frequency_penalty": {},
		"presence_penalty": {}, "repetition_penalty": {}, "max_tokens": {}, "max_completion_tokens": {}, "logit_bias": {},
		"logprobs": {}, "top_logprobs": {}, "prediction": {}, "seed": {}, "response_format": {}, "structured_outputs": {},
		"stop": {}, "tools": {}, "tool_choice": {}, "parallel_tool_calls": {}, "include_reasoning": {}, "reasoning": {},
		"reasoning_effort": {}, "web_search_options": {}, "verbosity": {},
	}
)

// queryMismatch is a query parameter Axiom reads but not this value of; queryUnsupported is one it cannot keep at all.
// Both name the parameter, as provider.go names the member of the provider object it refuses.
func queryMismatch(name, want string) *Refusal {
	return invalid("The " + name + " query parameter must be " + want + ".")
}

func queryUnsupported(name, reason string) *Refusal {
	return invalid("The " + name + " query parameter is not supported yet: " + reason + ".")
}

// keeps reports whether the query keeps the model: every filter it sets, and nothing else. A list of modalities or
// parameters keeps a model only when the model has all of them: a client that names images and files wants a model that
// takes both. A list of outputs keeps a model that answers text, or every model for "all"; Axiom answers nothing else.
func (q *orQuery) keeps(e *orModel) bool {
	switch {
	case q.search != "" && !strings.Contains(e.search, q.search):
		return false
	case e.ContextLength < q.context:
		return false
	case !q.prompt.keeps(e.prompt), !q.output.keeps(e.output):
		return false
	case !q.answersText():
		return false
	case len(q.authors) > 0 && !slices.Contains(q.authors, e.author):
		return false
	}
	return includes(e.Architecture.InputModalities, q.inputs) && includes(e.SupportedParameters, q.params)
}

// answersText reports whether the outputs the query names are ones Axiom's models answer. "all" is every output there is,
// and every model answers text; a filter on any other output keeps nothing, which is the truth about it.
func (q *orQuery) answersText() bool {
	return len(q.outputs) == 0 || slices.Contains(q.outputs, "all") || slices.Contains(q.outputs, "text")
}

// includes reports whether has every name want names.
func includes(has, want []string) bool {
	for _, name := range want {
		if !slices.Contains(has, name) {
			return false
		}
	}
	return true
}

// compare orders two models by the sort, and by their catalog names when they tie, so the same request always gets the
// same pages.
func (q *orQuery) compare(a, b *orModel) int {
	switch q.sort {
	case orCatalogOrder: // the list's own order, which a sorted page is never asked for: the names decide
	case orCheapestFirst:
		if c := a.average.Cmp(b.average); c != 0 {
			return c
		}
	case orDearestFirst:
		if c := b.average.Cmp(a.average); c != 0 {
			return c
		}
	case orLongestContext:
		if c := cmp.Compare(b.ContextLength, a.ContextLength); c != 0 {
			return c
		}
	}
	return strings.Compare(a.CanonicalSlug, b.CanonicalSlug)
}

// page is the range of matches the query returns: the offset into them and the one past the end. A query without a limit
// returns every match, as OpenRouter's does when neither offset nor limit is given.
func (q *orQuery) page(matched int) (first, last int) {
	first = min(q.offset, matched)
	last = matched
	if q.limit > 0 {
		last = min(first+q.limit, matched)
	}
	return first, last
}

// next is the link to the page after the one ending at last, or "" when that page is the last: the request's own query
// with the offset moved past the page, as OpenRouter's links.next does. The path is this list's own and url.Values.Encode
// writes ASCII, so the link needs no JSON escaping.
func (q *orQuery) next(last, matched int) string {
	if last >= matched {
		return ""
	}
	values := url.Values{} // the request's own query, with the offset moved; a query read from a request always has one
	maps.Copy(values, q.values)
	values.Set("offset", strconv.Itoa(last))
	path := q.path
	if path == "" {
		path = orListPath
	}
	return path + "?" + values.Encode()
}
