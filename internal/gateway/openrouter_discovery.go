package gateway

import (
	"net/http"
	"slices"
	"strings"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/routing"
)

// Discovery shares the model list's admission and publication. Supplier identities, credentials, upstream model names
// and costs never enter this projection; each public group endpoint is named by the group's stable codename under the
// public provider Axiom, the same identity provider.only, provider.ignore and presets route by.
func (h *Handler) discoverOpenRouter(w http.ResponseWriter, r *http.Request) {
	format := formatFor(r)
	release, ok := h.ipSlot(w, r, format)
	if !ok {
		return
	}
	defer release()
	v, err := h.visible(r)
	if err != nil {
		h.fail(w, r, format, err)
		return
	}
	l := h.Models.published()
	switch r.URL.Path {
	case orListPath + "/count":
		q, err := readORCountQuery(r)
		if err != nil {
			h.fail(w, r, format, err)
			return
		}
		writeJSON(w, map[string]any{"data": map[string]int{"count": l.count(v, q)}})
	case "/api/v1/providers":
		body := `{"data":[]}`
		if l.count(v, orQuery{}) > 0 {
			body = orProviders
		}
		writeBody(w, []byte(body))
	default:
		m, err := l.find(v, r.PathValue("author")+"/"+r.PathValue("slug"))
		if err != nil {
			h.fail(w, r, format, err)
			return
		}
		writeBody(w, m.endpoints)
	}
}

const orProviders = `{"data":[{"datacenters":null,"headquarters":null,"name":"Axiom","privacy_policy_url":null,"slug":"axiom","status_page_url":null,"terms_of_service_url":null}]}`

func (l *openRouterListing) count(v visibility, q orQuery) int {
	n := 0
	for i := range l.models {
		if e := l.models[i].forAudience(v.audience); e != nil && l.shows(v, e.model) && q.keeps(e) {
			n++
		}
	}
	return n
}

// Unknown nullable members remain null. Required nonnullable capability claims without data are omitted instead of
// presenting unknown as false or an empty set; gateway.md records these deliberate OpenRouter schema deviations.
type orEndpoint struct {
	Name                string    `json:"name"`
	ModelID             string    `json:"model_id"`
	ModelName           string    `json:"model_name"`
	ProviderName        string    `json:"provider_name"`
	Tag                 string    `json:"tag"`
	ContextLength       int64     `json:"context_length"`
	MaxCompletionTokens int64     `json:"max_completion_tokens"`
	MaxPromptTokens     any       `json:"max_prompt_tokens"`
	Pricing             orPricing `json:"pricing"`
	SupportedParameters []string  `json:"supported_parameters"`
	ServiceTier         any       `json:"service_tier"`
	Quantization        any       `json:"quantization"`
	Uptime5m            any       `json:"uptime_last_5m"`
	Uptime30m           any       `json:"uptime_last_30m"`
	Uptime1d            any       `json:"uptime_last_1d"`
	Latency             any       `json:"latency_last_30m"`
	Throughput          any       `json:"throughput_last_30m"`
}

type orEndpointModel struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Created      int64          `json:"created"`
	Architecture orArchitecture `json:"architecture"`
	Endpoints    []orEndpoint   `json:"endpoints"`
}

func encodeOREndpoints(m orEntry, endpoints []routing.Endpoint) ([]byte, error) {
	return encodeOREndpointList(m, endpoints, func(routing.Endpoint) (orPricing, bool) {
		return m.Pricing, true // a shared price version: every endpoint charges the model's price
	})
}

// encodeOREndpointPrices is the list for a version that prices each endpoint from its own cost: each endpoint's own
// published schedule, and an endpoint this version has no price for is left out. Supplier identity and cost never enter.
func encodeOREndpointPrices(m orEntry, sold billing.Sold, abilities catalog.AbilitySet, endpoints []routing.Endpoint) ([]byte, error) {
	return encodeOREndpointList(m, endpoints, func(e routing.Endpoint) (orPricing, bool) {
		price, ok := sold.PriceFor(uuid.UUID(e.ID))
		if !ok {
			return orPricing{}, false
		}
		return newORPricing(price, abilities), true
	})
}

func encodeOREndpointList(m orEntry, endpoints []routing.Endpoint, pricing func(routing.Endpoint) (orPricing, bool)) ([]byte, error) {
	data := orEndpointModel{
		ID: m.ID, Name: m.Name, Created: m.Created, Architecture: m.Architecture,
		Endpoints: make([]orEndpoint, 0, len(endpoints)),
	}
	// Sort a copy: the route builder owns the input. The codename is the public identity, so it orders the list and no
	// internal id reaches the response; an endpoint the version has no price for is left out before it gets a place.
	endpoints = slices.Clone(endpoints)
	slices.SortFunc(endpoints, func(a, b routing.Endpoint) int { return strings.Compare(a.Codename, b.Codename) })
	for _, e := range endpoints {
		price, ok := pricing(e)
		if !ok {
			continue
		}
		var quantization any
		if e.Quantization != "" {
			quantization = string(e.Quantization)
		}
		data.Endpoints = append(data.Endpoints, orEndpoint{
			Name: "Axiom: " + m.Name, ModelID: m.ID, ModelName: m.Name, ProviderName: "Axiom", Tag: e.Codename,
			ContextLength: e.Context, MaxCompletionTokens: e.MaxOutput, Pricing: price, SupportedParameters: m.SupportedParameters,
			Quantization: quantization,
		})
	}
	return jsonx.MarshalCanonical(struct {
		Data orEndpointModel `json:"data"`
	}{Data: data})
}
