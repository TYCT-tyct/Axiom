package gateway

import (
	"slices"
	"strings"

	"axiom/internal/billing"
	"axiom/internal/catalog"
)

// PublicModel is one model of the public models and prices page: what a visitor may see of the catalog, at the sale
// price the audience pays. It carries no supplier, endpoint, route or cost, and no internal name or price source: the
// page is built from the same snapshot the router publishes, so its price is the price a call is charged at.
type PublicModel struct {
	ID            string
	Name          string
	Author        string
	ContextLength int64
	MaxOutput     int64
	Streams       bool
	Abilities     []string
	Pricing       catalog.ScheduleView
}

// Public is the models the last build serves, at a's published price, sorted by author and public ID. A model with no
// published price for a is left out, as the OpenRouter list leaves it out for a key: the page must not offer a model
// that cannot be called at the price shown.
func (m *ModelList) Public(a billing.Audience) []PublicModel {
	l := m.published()
	models := make([]PublicModel, 0, len(l.models))
	for i := range l.models {
		entry := &l.models[i]
		sold := entry.sold.For(a)
		if !sold.Available() {
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
			Pricing:       sold.Schedule.View(),
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
