package gateway

import (
	"cmp"
	"net/http"
	"strconv"
	"strings"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// modelName is a model name from a client read apart: the name the catalog is asked for and what its variants ask of
// the route. A variant is a routing switch written after the model (author/model:nitro), never a model of its own; the
// one catalog suffix is :free, which names its own entry and stays on the base (docs/domains/gateway.md「模型名变体」).
// The client's own spelling stays in the request and the call record; a successful response names the model that
// actually served it.
type modelName struct {
	base     string
	sort     *routing.Strategy // the ranking the last sorting variant asks for, or nil
	exacto   bool              // the last sorting variant is :exacto: quality first, among endpoints verified as the model
	thinking bool              // the deprecated :thinking, which only a request that asks for reasoning itself honours
	online   bool              // the deprecated :online web plugin
	auto     bool              // openrouter/auto: the routing ranking chooses the model
	free     bool              // openrouter/free: the routing ranking chooses among the free catalog entries
}

// variant is what one documented suffix asks of the request (OpenRouter《Model Variants》, read 2026-10-29). A suffix
// is never a model of its own; :free is the one suffix that names a catalog entry and remains part of the base. What
// Axiom does with each is in docs/domains/gateway.md「模型名变体」.
type variant struct {
	sort     string // the provider.sort it stands for, "" for none
	exacto   bool   // the Exacto ranking: quality first, among endpoints verified as the model they claim
	thinking bool   // reasoning on, which the request must already ask for
	free     bool   // the catalog's own :free entry, which stays on the base
	online   bool   // the deprecated :online web plugin
	refusal  string // what every request with this suffix gets, "" when it can be honoured
}

const (
	errExactoAlternate = "A sorting variant on an alternate model must match the request's ranking: the ranking is one for the whole request, and :exacto on an alternate needs the first model to rank the same way."
	errAlternateSort   = "A sorting variant on an alternate model must match the request's ranking: the ranking is one for the whole request."
	errOnline          = "The :online variant is the deprecated web plugin, and web search is not supported here: " + protocol.NoWebSearch + ". Use a model's native web search tool."
	errThinking        = "The :thinking variant is deprecated by OpenRouter: ask for reasoning with the reasoning parameter, which also enables it. A request that already asks for reasoning may keep the suffix."
	errBatch           = "The :batch variant is a separate batch-priced catalog entry on OpenRouter, and this catalog has none."
	errExtended        = "The :extended variant is deprecated by OpenRouter and no model offers it any more; this catalog has none."
)

// variants are the suffixes OpenRouter documents: :nitro and :floor rank endpoints, :exacto ranks verified endpoints by
// quality, :thinking is the deprecated way to ask for reasoning, :online is the deprecated web plugin, :free names the
// catalog's own free entry, and :batch and :extended are catalog entries this catalog does not hold. One table, so what
// a suffix does and why it is refused are read together.
var variants = map[string]variant{
	"nitro": {sort: "throughput"}, "floor": {sort: "price"},
	"exacto": {exacto: true}, "thinking": {thinking: true},
	"online": {online: true}, "free": {free: true},
	"batch": {refusal: errBatch}, "extended": {refusal: errExtended},
}

// maxModelName bounds the whole name a client writes, variants included, in bytes. The call record keeps that name and
// its column takes at most 200 characters (internal/observability/migrations/00001_call_log.sql), and the catalog took
// no name longer than this either, so a name the catalog would have refused before variants is still refused here,
// before anything is reserved, however many variants are stacked on a short base.
const maxModelName = 200

func noVariant(message string) *Refusal {
	return &Refusal{Status: http.StatusNotFound, Code: errNoModel.Code, Message: message}
}

// parseModelName reads the variants after the model. They may come in any order, the last sorting one deciding the
// ranking. A variant this service cannot honor is a 400, as an unsupported provider member is: a request is never
// served without what it asked for. :free stays in the base name, because it is the catalog's own entry, and a base
// without that entry is a 404 like any other unknown model. An unknown suffix is a 404, whether or not the base
// exists. :exacto is left to rank, because an explicit provider.sort replaces it (OpenRouter《Exacto》, 2026-10-29).
// :thinking is left to the caller, which alone knows whether the request asks for reasoning already: discovery answers
// the base model, a call refuses a suffix nothing there enables. ~author/family-latest is a catalog alias, so it is
// left whole for the catalog to resolve.
func parseModelName(name string) (modelName, *Refusal) {
	if len(name) > maxModelName {
		return modelName{}, noVariant("The model name is longer than the " + strconv.Itoa(maxModelName) + " bytes a model name may have.")
	}
	base, rest, more := strings.Cut(name, ":")
	m := modelName{base: base}
	for more {
		var suffix string
		suffix, rest, more = strings.Cut(rest, ":")
		v, ok := variants[strings.ToLower(strings.TrimSpace(suffix))]
		switch {
		case !ok:
			return modelName{}, noVariant("The model name has a suffix after ':' that is not a model variant.")
		case v.refusal != "":
			return modelName{}, invalid(v.refusal)
		case v.free:
			m.base += ":free"
		case v.sort != "":
			s := strategies[v.sort]
			m.sort, m.exacto = &s, false
		case v.exacto:
			m.sort, m.exacto = nil, true
		case v.thinking:
			m.thinking = true
		case v.online:
			m.online = true
		}
	}
	m.auto = strings.EqualFold(m.base, "openrouter/auto")
	m.free = strings.EqualFold(m.base, "openrouter/free")
	return m, nil
}

// variantNames are the models a request names, by the base names the catalog is asked for.
type variantNames struct {
	primary    string   // the base name of the model the request names
	alternates []string // the base names of the models it lists that could be tried
	auto       bool     // the request named openrouter/auto, which the routing ranking expands
	online     bool     // the request asked for the deprecated :online web plugin on any of its models
	free       bool     // the request named openrouter/free, which the routing ranking expands over the free entries
}

// routerName is the router the request named, "" when it named a model instead.
func (v variantNames) routerName() string {
	switch {
	case v.auto:
		return "openrouter/auto"
	case v.free:
		return "openrouter/free"
	}
	return ""
}

// readRouting reads what a request's provider object and model names ask of the route, for a call and for a count alike,
// so they refuse alike. The ranking is an explicit provider.sort first, as OpenRouter says of :exacto ("an explicit
// sort ... overrides it"; for :nitro and :floor it is silent, and they are treated alike), then the first name's
// variant. A variant that cannot be honored is refused, alternates' included, so none is ignored: an :exacto only an
// explicit provider.sort replaces (a sort variant on the first name does not rank an alternate's quality), and a sorting
// variant on an alternate that differs from the ranking the request gets, as routing ranks per request. An alternate
// with an unknown suffix is left out, as it could not be tried; a known unsupported variant is refused.
func readRouting(req *protocol.Request, body []byte) (routing.Override, variantNames, error) {
	over, err := override(req.Provider)
	if err != nil {
		return over, variantNames{}, err
	}
	if err := requireParameters(req, body, &over); err != nil {
		return over, variantNames{}, err
	}
	primary, ref := parseModelName(req.Model)
	if ref != nil {
		return over, variantNames{}, ref
	}
	explicit := over.Sort != nil
	verified := false
	if primary.exacto && !explicit {
		quality := routing.BestQuality
		over.Sort, over.VerifiedOnly = &quality, true
		verified = true
	}
	// The deprecated :thinking only repeats what the request itself asks for; a suffix nothing enables would serve a
	// model that does not think, unlike what the client asked for.
	reasoning := req.Needs.Has(protocol.Reasoning)
	if primary.thinking && !reasoning {
		return over, variantNames{}, invalid(errThinking)
	}
	over.Sort = cmp.Or(over.Sort, primary.sort)
	v := variantNames{primary: primary.base, auto: primary.auto, online: primary.online, free: primary.free}
	for _, name := range req.Models {
		base, online, ok, err := alternate(name, explicit, over.Sort, verified, reasoning)
		if err != nil {
			return over, variantNames{}, err
		}
		if ok {
			v.alternates = append(v.alternates, base)
			v.online = v.online || online
		}
	}
	return over, v, nil
}

// requireParameters settles what provider.require_parameters asks for: the parameter inventory the request wrote
// outside, and the parameters an endpoint must then report.
func requireParameters(req *protocol.Request, body []byte, over *routing.Override) error {
	if !over.RequireParameters {
		return nil
	}
	params, unknown, found := protocol.RequestParameters(req.Format, body)
	if found {
		return invalid("provider.require_parameters cannot be kept: no endpoint parameter inventory records \"" + unknown + "\"; supplier model records and catalog specs carry no sampling or provider parameters.")
	}
	over.RequiredParameters = params
	return nil
}

// alternate reads one name of the request's models under the ranking the request has. ok is false for a name that
// could not be tried. explicit says the request wrote a provider.sort, the only thing that replaces an :exacto here;
// verified says the ranking the request already has is quality-first among verified endpoints, which is what an
// alternate's :exacto stands for; reasoning says the request asks for reasoning, without which an alternate's :thinking
// asks for nothing.
func alternate(name string, explicit bool, ranking *routing.Strategy, verified, reasoning bool) (base string, online, ok bool, err error) {
	alt, ref := parseModelName(name)
	if ref != nil {
		if ref.Status == http.StatusNotFound {
			return "", false, false, nil
		}
		return "", false, false, ref
	}
	if alt.auto || alt.free {
		// auto already considers every model the key may call and free every free one; a second such name adds
		// nothing to try.
		return "", false, false, nil
	}
	if err := alternateRefusal(alt, explicit, ranking, verified, reasoning); err != nil {
		return "", false, false, err
	}
	return alt.base, alt.online, true, nil
}

// alternateRefusal is why an alternate's variants cannot be honoured under the request's ranking, or nil.
func alternateRefusal(alt modelName, explicit bool, ranking *routing.Strategy, verified, reasoning bool) error {
	switch {
	case alt.exacto && !explicit && !verified:
		return invalid(errExactoAlternate)
	case alt.thinking && !reasoning:
		return invalid(errThinking)
	case alt.sort != nil && (ranking == nil || *ranking != *alt.sort):
		return invalid(errAlternateSort)
	}
	return nil
}

// resolve turns the model the request names, then the alternates it lists, into models. The first must exist; an
// alternate that does not is left out, as it could not be tried.
func resolve(snap *catalog.Snapshot, v variantNames) ([]catalog.ModelID, error) {
	first, err := snap.ResolvePublic(v.primary)
	if err != nil {
		return nil, err
	}
	models := []catalog.ModelID{first.Model}
	for _, name := range v.alternates {
		if m, err := snap.ResolvePublic(name); err == nil {
			models = append(models, m.Model)
		}
	}
	return models, nil
}

// resolveModels names the catalog models a request may call: the ones it names, or none for a router, whose model
// routing ranks. Compression needs one model's context window, which a router names only after it routes.
func resolveModels(snap *catalog.Snapshot, v variantNames, req *protocol.Request) ([]catalog.ModelID, error) {
	if v.routerName() == "" {
		return resolve(snap, v)
	}
	if req.Compression == protocol.CompressionEnabled {
		return nil, invalid(v.routerName() + " chooses a model while routing, so context-compression cannot know its context window. Name the model or leave the plugin out.")
	}
	return nil, nil
}
