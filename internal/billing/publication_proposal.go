package billing

import (
	"context"
	"errors"
	"maps"
	"slices"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func proposeAudiencePrice(audience Selector, policy PricePolicy, rules []Rule, m catalog.Model, snap *catalog.Snapshot, currency platform.Currency, costs []PriceCost, defaults fallbacks, rates ...platform.ExchangeRate) (Publication, error) {
	p, err := proposePrice(policy, rules, m, snap, currency, costs, defaults, rates...)
	if err != nil {
		return p, err
	}
	p.Audience = audience
	// The default table already binds its audience; keep its pre-audience digest and audited exceptions stable.
	if audience.Kind == "all" {
		return p, nil
	}
	p.Candidate = uuid.Nil()
	raw, err := jsonx.MarshalCanonical(p)
	if err != nil {
		return p, err
	}
	p.Candidate = digestPrice(raw)
	return p, nil
}

func proposePrice(policy PricePolicy, rules []Rule, m catalog.Model, snap *catalog.Snapshot, currency platform.Currency, costs []PriceCost, defaults fallbacks, rates ...platform.ExchangeRate) (Publication, error) {
	p := Publication{Model: m.ID, Status: "unpriced"}
	envelope, costErr := modelPriceCost(costs, m, currency)
	input, matched, ref, err := priceFromRules(rules, m, snap, envelope, costErr, currency, rates)
	p.matched = matched != nil
	if matched != nil && matched.Method.Kind != "reference_multiplier" {
		ref.ID = uuid.Nil()
	}
	if err != nil {
		p.Problem = err.Error()
	}
	p.Basis, err = priceEvidence(policy, matched, ref.ID, costs, referenceExchange(ref, currency, rates))
	if err != nil {
		return p, err
	}
	if matched != nil && p.Problem == "" {
		switch {
		case input.Priced(1<<catalog.ItemFileSearch) && !m.Reportable().Items.Has(catalog.ItemFileSearch):
			// A rule or reference may price an item the model does not sell. Publishing that price would put a new
			// item's name into a version every node reads, for a model that never serves it.
			p.Problem = "the price names file_search, which the model does not declare"
			p.Schedule, p.Floor = nil, nil
		default:
			if err := p.protectPrice(input, envelope, costErr, policy, defaults, m.Free()); err != nil {
				p.Problem = err.Error()
				p.Schedule = nil
				p.Floor = nil
			}
		}
	}
	raw, err := jsonx.MarshalCanonical(p)
	if err != nil {
		return p, err
	}
	p.Candidate = digestPrice(raw)
	return p, nil
}

func modelPriceCost(costs []PriceCost, m catalog.Model, currency platform.Currency) (catalog.Schedule, error) {
	for _, c := range costs {
		if Unpriced(c.Schedule, m.Reportable()) != (catalog.Reportable{}) {
			return catalog.Schedule{}, ErrUnpriced
		}
	}
	return CostEnvelope(costs, currency)
}

func priceFromRules(rules []Rule, m catalog.Model, snap *catalog.Snapshot, envelope catalog.Schedule, costErr error, currency platform.Currency, rates []platform.ExchangeRate) (catalog.Schedule, *Rule, catalog.ReferencePrice, error) {
	for _, r := range rules {
		if !r.Enabled || !pricesModel(r.Target, m) {
			continue
		}
		ref := saleReference(snap, m.ID, currency, r.Method)
		if r.Method.Kind == "cost_plus" && r.Constraints == (Constraints{}) {
			if costErr != nil {
				return catalog.Schedule{}, &r, ref, costErr
			}
			s, err := costPrice(r.Method, envelope, currency)
			return s, &r, ref, err
		}
		s, err := Sell(r, ref, currency, rates...)
		if errors.Is(err, ErrUnpriced) {
			continue
		}
		if errors.Is(err, ErrRuleRefuses) {
			return catalog.Schedule{}, &r, ref, &RuleRefusal{Rule: r.ID, Model: m.ID}
		}
		return s, &r, ref, err
	}
	return catalog.Schedule{}, nil, catalog.ReferencePrice{}, ErrUnpriced
}

// ReferenceFor is the reference price a reference-multiplier rule reads for a model: the site's currency first, then
// USD or the supported currencies when conversion is allowed. It is exported so a caller outside billing can price the
// same row the sale rules read.
func ReferenceFor(snap *catalog.Snapshot, model catalog.ModelID, currency platform.Currency, conversion string) catalog.ReferencePrice {
	return saleReference(snap, model, currency, Method{Kind: "reference_multiplier", CurrencyConversion: conversion})
}

func saleReference(snap *catalog.Snapshot, model catalog.ModelID, currency platform.Currency, method Method) catalog.ReferencePrice {
	if method.Kind != "reference_multiplier" {
		return catalog.ReferencePrice{}
	}
	if ref, ok := snap.ReferencePrice(model, currency.String()); ok {
		return ref
	}
	if method.CurrencyConversion != "exchange" {
		return catalog.ReferencePrice{}
	}
	if ref, ok := snap.ReferencePrice(model, "USD"); ok {
		return ref
	}
	for _, code := range slices.Sorted(maps.Keys(minorDigits)) {
		if ref, ok := snap.ReferencePrice(model, code); ok {
			return ref
		}
	}
	return catalog.ReferencePrice{}
}

func referenceExchange(ref catalog.ReferencePrice, currency platform.Currency, rates []platform.ExchangeRate) *platform.ExchangeRate {
	if ref.ID == uuid.Nil() || ref.Schedule.Currency() == currency {
		return nil
	}
	for _, r := range rates {
		if _, ok := r.Between(ref.Schedule.Currency(), currency); ok {
			return &r
		}
	}
	return nil
}

func priceEvidence(policy PricePolicy, rule *Rule, reference uuid.UUID, costs []PriceCost, exchange *platform.ExchangeRate) (jsonx.Raw, error) {
	return jsonx.MarshalCanonical(struct {
		Policy    PricePolicy            `json:"policy"`
		Rule      *Rule                  `json:"rule"`
		Reference uuid.UUID              `json:"reference"`
		Costs     []PriceCost            `json:"costs"`
		Exchange  *platform.ExchangeRate `json:"reference_exchange,omitempty"`
	}{policy, rule, reference, costs, exchange})
}

func (p *Publication) protectPrice(input, envelope catalog.Schedule, costErr error, policy PricePolicy, defaults fallbacks, free bool) error {
	input = input.Fill(defaults.perUse)
	if free && !input.IsZero() {
		return ErrFreePrice
	}
	var err error
	p.Requested, err = input.MarshalJSON()
	if err != nil {
		return err
	}
	if costErr != nil {
		return costErr
	}
	p.Cost, err = envelope.MarshalJSON()
	if err != nil {
		return err
	}
	floor, err := envelope.Scaled(policy.FloorMultiplier, input.Currency())
	if err != nil {
		return err
	}
	p.Floor, err = floor.MarshalJSON()
	if err != nil {
		return err
	}
	input, err = input.Aligned(floor, false)
	if err != nil {
		return err
	}
	if free && !input.IsZero() {
		return ErrFreePrice
	}
	p.Schedule, err = input.MarshalJSON()
	if err != nil {
		return err
	}
	p.Status = "pending"
	return nil
}

func priceDefaults(ctx context.Context, tx pg.Tx, currency platform.Currency) (fallbacks, error) {
	rows, err := queries.ReadFallbacks(ctx, tx, []string{currency.String()})
	if err != nil {
		return fallbacks{}, err
	}
	if len(rows) == 0 {
		return fallbacks{}, ErrUnpriced
	}
	return parseFallbacks(rows[0].Body)
}
