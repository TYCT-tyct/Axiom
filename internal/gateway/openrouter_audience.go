package gateway

import (
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/routing"
)

func newAudienceORModel(id string, m catalog.Model, serving catalog.Serving, sold billing.Sold, served []uuid.UUID) (orModel, bool, error) {
	if sold.Audiences == nil {
		price, ok := sold.ModelPrice(served)
		if !ok {
			return orModel{}, false, nil
		}
		e, ok, err := newORModel(id, m, serving, price)
		if err != nil || !ok {
			return orModel{}, ok, err
		}
		e.sold, e.served, e.serving = sold, served, serving
		return e, true, nil
	}
	variants := make([]orModel, len(sold.Audiences.Layers()))
	var base orModel
	for i, price := range sold.Audiences.Layers() {
		schedule, ok := price.Sold.ModelPrice(served)
		if !ok {
			continue
		}
		e, ok, err := newORModel(id, m, serving, schedule)
		if err != nil {
			return orModel{}, false, err
		}
		if ok {
			e.sold, e.served, e.serving = price.Sold, served, serving
			variants[i] = e
			base = orModel{model: e.model, author: e.author, search: e.search}
			// The public page shows the model once, not once per audience layer: its name and limits do not change with
			// the price, so the base entry carries them.
			base.ID, base.Name = e.ID, e.Name
		}
	}
	if base.search == "" {
		return orModel{}, false, nil
	}
	base.audiences, base.variants = sold.Audiences, variants
	base.sold, base.served, base.serving = sold, served, serving
	return base, true, nil
}

func (m *orModel) forAudience(a billing.Audience) *orModel {
	if m.audiences == nil {
		return m
	}
	at := m.audiences.Select(a)
	if at < 0 || len(m.variants[at].body) == 0 {
		return nil
	}
	return &m.variants[at]
}

func (m *orModel) encodeEndpoints(endpoints []routing.Endpoint) error {
	encode := func(e *orModel) error {
		var err error
		if e.sold.Endpoints == nil {
			e.endpoints, err = encodeOREndpoints(e.orEntry, endpoints)
		} else {
			e.endpoints, err = encodeOREndpointPrices(e.orEntry, e.sold, e.serving.Abilities, endpoints)
		}
		return err
	}
	if m.audiences == nil {
		return encode(m)
	}
	for i := range m.variants {
		variant := &m.variants[i]
		if len(variant.body) == 0 {
			continue
		}
		if err := encode(variant); err != nil {
			return err
		}
	}
	return nil
}
