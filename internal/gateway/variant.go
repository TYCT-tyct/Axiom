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
// the route. A variant is a routing switch written after the model (author/model:nitro), never a model of its own, so
// the catalog is only ever asked for base and still finds no name with a ':' (docs/domains/gateway.md「模型名变体」).
// The client's own spelling stays in the request, which is what the response and the call record echo.
type modelName struct {
	base   string
	sort   *routing.Strategy // the ranking the last sorting variant asks for, or nil
	exacto bool              // the last sorting variant is :exacto, which no ranking here serves
}

// sortVariants are the variants that rank endpoints, each as the provider.sort it stands for (OpenRouter《Model
// Variants》, 2026-10-19: :nitro sorts by throughput and :floor by price).
var sortVariants = map[string]string{"nitro": "throughput", "floor": "price"}

// catalogVariants need separate entries and prices. Refuse them explicitly until the catalog can represent them:
// stripping :free and serving the paid base model would break the request's price constraint.
var catalogVariants = map[string]bool{"free": true, "batch": true, "extended": true}

const (
	errExacto = "The :exacto variant is not supported yet: it ranks endpoints by measured tool-calling quality, which is not measured here. " +
		"Use :nitro, :floor or provider.sort."
	errOnline = "The :online variant is not supported yet: it is the web search plugin, which is not available here."
	errAlias  = "Model aliases such as ~author/family-latest are not supported: this catalog has no model-family mappings; use a concrete model name."
)

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
// served without what it asked for. A known catalog variant cannot be represented here and is a 400; an unknown suffix is a 404,
// whether or not the base exists. :exacto is left to rank, because an explicit provider.sort replaces it
// (OpenRouter《Exacto》, 2026-10-19).
func parseModelName(name string) (modelName, *Refusal) {
	if len(name) > maxModelName {
		return modelName{}, noVariant("The model name is longer than the " + strconv.Itoa(maxModelName) + " bytes a model name may have.")
	}
	base, rest, more := strings.Cut(name, ":")
	if strings.HasPrefix(strings.TrimSpace(base), "~") {
		return modelName{}, invalid(errAlias)
	}
	m := modelName{base: base}
	for more {
		var suffix string
		suffix, rest, more = strings.Cut(rest, ":")
		suffix = strings.ToLower(strings.TrimSpace(suffix))
		if by, ok := sortVariants[suffix]; ok {
			s := strategies[by]
			m.sort, m.exacto = &s, false
			continue
		}
		switch {
		case suffix == "exacto":
			m.sort, m.exacto = nil, true
		case suffix == "online":
			return modelName{}, invalid(errOnline)
		case suffix == "thinking":
			return modelName{}, invalid("The :thinking catalog variant is not supported; ask for reasoning with the reasoning parameter.")
		case catalogVariants[suffix]:
			return modelName{}, invalid("The :" + suffix + " catalog variant is not supported: this catalog has no separate variant entries or prices.")
		default:
			return modelName{}, noVariant("The model name has a suffix after ':' that is not a model variant.")
		}
	}
	return m, nil
}

// variantNames are the models a request names, by the base names the catalog is asked for.
type variantNames struct {
	primary    string   // the base name of the model the request names
	alternates []string // the base names of the models it lists that could be tried
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
	if over.RequireParameters {
		params, unknown := protocol.RequestParameters(body)
		if unknown != "" {
			return over, variantNames{}, invalid("provider.require_parameters cannot be kept: no endpoint parameter inventory records \"" + unknown + "\"; supplier model records and catalog specs carry no sampling or provider parameters.")
		}
		over.RequiredParameters = params
	}
	primary, ref := parseModelName(req.Model)
	if ref != nil {
		return over, variantNames{}, ref
	}
	explicit := over.Sort != nil
	if primary.exacto && !explicit {
		return over, variantNames{}, invalid(errExacto)
	}
	over.Sort = cmp.Or(over.Sort, primary.sort)
	v := variantNames{primary: primary.base}
	for _, name := range req.Models {
		base, ok, err := alternate(name, explicit, over.Sort)
		if err != nil {
			return over, variantNames{}, err
		}
		if ok {
			v.alternates = append(v.alternates, base)
		}
	}
	return over, v, nil
}

// alternate reads one name of the request's models under the ranking the request has. ok is false for a name that
// could not be tried. explicit says the request wrote a provider.sort, the only thing that replaces an :exacto here.
func alternate(name string, explicit bool, ranking *routing.Strategy) (base string, ok bool, err error) {
	alt, ref := parseModelName(name)
	switch {
	case ref != nil && ref.Status == http.StatusNotFound:
		return "", false, nil
	case ref != nil:
		return "", false, ref
	case alt.exacto && !explicit:
		return "", false, invalid(errExacto)
	case alt.sort != nil && (ranking == nil || *ranking != *alt.sort):
		return "", false, invalid("A sorting variant on an alternate model must match the request's ranking: the ranking is one for the whole request.")
	}
	return alt.base, true, nil
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
