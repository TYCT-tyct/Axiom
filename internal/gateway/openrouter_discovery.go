package gateway

import (
	"bytes"
	"net/http"
	"slices"
	"strconv"

	"axiom/internal/platform/jsonx"
	"axiom/internal/routing"
)

// Discovery shares the model list's admission and publication. Supplier identities, credentials, upstream model names
// and costs never enter this projection; each public group endpoint has a model-local ordinal under the public provider Axiom.
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
	data := orEndpointModel{
		ID: m.ID, Name: m.Name, Created: m.Created, Architecture: m.Architecture,
		Endpoints: make([]orEndpoint, 0, len(endpoints)),
	}
	// Sort a copy: the route builder owns the input. Only ordinals cross the public boundary.
	endpoints = slices.Clone(endpoints)
	slices.SortFunc(endpoints, func(a, b routing.Endpoint) int { return bytes.Compare(a.ID[:], b.ID[:]) })
	for i, e := range endpoints {
		var quantization any
		if e.Quantization != "" {
			quantization = string(e.Quantization)
		}
		data.Endpoints = append(data.Endpoints, orEndpoint{
			Name: "Axiom: " + m.Name, ModelID: m.ID, ModelName: m.Name, ProviderName: "Axiom", Tag: "axiom/" + strconv.Itoa(i+1),
			ContextLength: e.Context, MaxCompletionTokens: e.MaxOutput, Pricing: m.Pricing, SupportedParameters: m.SupportedParameters,
			Quantization: quantization,
		})
	}
	return jsonx.MarshalCanonical(struct {
		Data orEndpointModel `json:"data"`
	}{Data: data})
}
