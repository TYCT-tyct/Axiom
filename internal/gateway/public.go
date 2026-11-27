package gateway

import (
	"slices"
	"strings"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/quality"
	"axiom/internal/routing"
)

// PublicEndpoint is one endpoint of a public model: the group's public identity, the limits and abilities it serves,
// and the sale price this audience pays at it. The identity is the group's stable codename, the same value
// provider.only, provider.ignore and presets accept; a directly connected official API will show its real name there.
// No supplier, upstream name, internal id or cost is part of it.
type PublicEndpoint struct {
	Name          string
	ContextLength int64
	MaxOutput     int64
	Streams       bool
	Abilities     []string
	Quantization  string
	Verified      bool
	Pricing       catalog.ScheduleView
	// SourceClass is the slug of the source class quality recognized; Score the product score, 0 to 100 with the
	// official direct run at 100, nil while no run counts for it; Recent its uptime, time to first token and speed.
	SourceClass string
	Score       *float64
	Recent      quality.Recent
}

// PublicModel is one model of the public models and prices page: what a visitor may see of the catalog, at the sale
// price the audience pays. It carries no supplier, route or cost, and no internal name or price source: the page is
// built from the same snapshot the router publishes, so its price is the price a call is charged at. Each endpoint is
// listed with its own price, which may differ from the model's cheapest one.
type PublicModel struct {
	ID            string
	Name          string
	Author        string
	ContextLength int64
	MaxOutput     int64
	Streams       bool
	Abilities     []string
	Pricing       catalog.ScheduleView
	Endpoints     []PublicEndpoint
}

// publicEndpoint is what the page keeps of a served endpoint from the build: the audience-independent attributes. The
// price is looked up when a page is made, because which endpoints an audience may buy is a property of its price layer.
type publicEndpoint struct {
	id            uuid.UUID
	name          string
	contextLength int64
	maxOutput     int64
	streams       bool
	abilities     []string // nil when the endpoint was built without per-format specs
	quantization  string
	verified      bool
	class         string
	score         *float64
	recent        quality.Recent
}

// Public is the models the last build serves, at a's published price, sorted by author and public ID. A model with no
// published price for a is left out, as the OpenRouter list leaves it out for a key: the page must not offer a model
// that cannot be called at the price shown. Each endpoint is listed at its own price; an endpoint the audience's price
// version does not price is left out, so the list never offers an endpoint that cannot be bought.
func (m *ModelList) Public(a billing.Audience) []PublicModel {
	l := m.published()
	models := make([]PublicModel, 0, len(l.models))
	for i := range l.models {
		entry := &l.models[i]
		sold := entry.sold.For(a)
		price, ok := sold.ModelPrice(entry.served)
		if !sold.Available() || !ok {
			continue
		}
		abilities := make([]string, 0, len(entry.serving.Abilities.List()))
		for _, ability := range entry.serving.Abilities.List() {
			abilities = append(abilities, ability.String())
		}
		models = append(models, PublicModel{
			ID:            entry.ID,
			Name:          entry.Name,
			Author:        entry.author,
			ContextLength: entry.serving.Context,
			MaxOutput:     entry.serving.MaxOutput,
			Streams:       entry.serving.Streams,
			Abilities:     abilities,
			Pricing:       price.View(),
			Endpoints:     publicEndpointsOfSold(entry, sold, abilities),
		})
	}
	slices.SortFunc(models, func(a, b PublicModel) int {
		if c := strings.Compare(strings.ToLower(a.Author), strings.ToLower(b.Author)); c != 0 {
			return c
		}
		return strings.Compare(strings.ToLower(a.ID), strings.ToLower(b.ID))
	})
	return models
}

// publicEndpointsOfSold projects the endpoints a price version can sell to the audience, each with the endpoint's
// effective public surface. The model-level abilities are the fallback for a page built before endpoints had specs.
func publicEndpointsOfSold(entry *orModel, sold billing.Sold, abilities []string) []PublicEndpoint {
	endpoints := make([]PublicEndpoint, 0, len(entry.pageEndpoints))
	for _, e := range entry.pageEndpoints {
		endpointPrice, ok := sold.PriceFor(e.id)
		if !ok {
			continue
		}
		endpointAbilities := e.abilities
		if endpointAbilities == nil {
			endpointAbilities = abilities
		}
		endpoints = append(endpoints, PublicEndpoint{
			Name: e.name, ContextLength: e.contextLength, MaxOutput: e.maxOutput, Streams: e.streams,
			Abilities: endpointAbilities, Quantization: e.quantization, Verified: e.verified, Pricing: endpointPrice.View(),
			SourceClass: e.class, Score: e.score, Recent: e.recent,
		})
	}
	return endpoints
}

// publicEndpointsOf keeps the audience-independent attributes of a model's served endpoints for the public page. The
// codename is the public identity and cannot repeat within a model, so it gives the page one stable order that an
// internal id never influences.
func publicEndpointsOf(endpoints []routing.Endpoint) []publicEndpoint {
	page := make([]publicEndpoint, len(endpoints))
	for i, e := range endpoints {
		context, maxOutput := e.Context, e.MaxOutput
		var abilities []string
		if len(e.Specs) > 0 {
			surface := e.Surface()
			context, maxOutput, abilities = surface.Context, surface.MaxOutput, abilityNames(surface.Caps)
		}
		page[i] = publicEndpoint{
			id: uuid.UUID(e.ID), name: e.Codename, contextLength: context, maxOutput: maxOutput,
			streams: e.Streams, abilities: abilities, quantization: string(e.Quantization), verified: e.Verified,
			class: string(e.Class), score: measuredScore(e), recent: e.Recent,
		}
	}
	slices.SortFunc(page, func(a, b publicEndpoint) int { return strings.Compare(a.name, b.name) })
	return page
}

// measuredScore is e's product score, nil when no run counts for it: the confidence says whether there is one, and a
// valid run can score 0.
func measuredScore(e routing.Endpoint) *float64 {
	if e.ScoreConfidence <= 0 {
		return nil
	}
	return &e.Score
}
