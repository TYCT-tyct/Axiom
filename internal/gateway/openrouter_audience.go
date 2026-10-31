package gateway

import (
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/routing"
)

func newAudienceORModel(id string, m catalog.Model, serving catalog.Serving, sold billing.Sold) (orModel, bool, error) {
	if sold.Audiences == nil {
		return newORModel(id, m, serving, sold.Schedule)
	}
	variants := make([]orModel, len(sold.Audiences.Layers()))
	var base orModel
	for i, price := range sold.Audiences.Layers() {
		e, ok, err := newORModel(id, m, serving, price.Sold.Schedule)
		if err != nil {
			return orModel{}, false, err
		}
		if ok {
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
	var err error
	if m.audiences == nil {
		m.endpoints, err = encodeOREndpoints(m.orEntry, endpoints)
		return err
	}
	for i := range m.variants {
		variant := &m.variants[i]
		if len(variant.body) == 0 {
			continue
		}
		variant.endpoints, err = encodeOREndpoints(variant.orEntry, endpoints)
		if err != nil {
			return err
		}
	}
	return nil
}
