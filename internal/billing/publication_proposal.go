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

// saleProposal is what a rule asked for before the floor: the model-level price, and, when it prices each endpoint
// from that endpoint's own cost, every endpoint's price and the converted cost it came from.
type saleProposal struct {
	requested catalog.Schedule
	endpoints EndpointPrices
	costs     EndpointPrices
}

func proposePrice(policy PricePolicy, rules []Rule, m catalog.Model, snap *catalog.Snapshot, currency platform.Currency, costs []PriceCost, defaults fallbacks, rates ...platform.ExchangeRate) (Publication, error) {
	p := Publication{Model: m.ID, Status: "unpriced"}
	converted, envelope, costErr := modelPriceCost(costs, m, currency)
	input, matched, ref, err := priceFromRules(rules, m, snap, converted, costErr, currency, rates)
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
		case input.requested.Priced(1<<catalog.ItemFileSearch) && !m.Reportable().Items.Has(catalog.ItemFileSearch):
			// A rule or reference may price an item the model does not sell. Publishing that price would put a new
			// item's name into a version every node reads, for a model that never serves it.
			p.Problem = "the price names file_search, which the model does not declare"
			p.Schedule, p.Floor = nil, nil
		default:
			if err := p.protectPrice(input, envelope, costErr, policy, defaults, m.Free()); err != nil {
				p.Problem = err.Error()
				p.Schedule, p.Floor = nil, nil
				p.Endpoints, p.RequestedEndpoints, p.Costs, p.Floors = nil, nil, nil, nil
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

func modelPriceCost(costs []PriceCost, m catalog.Model, currency platform.Currency) (EndpointPrices, catalog.Schedule, error) {
	for _, c := range costs {
		if Unpriced(c.Schedule, m.Reportable()) != (catalog.Reportable{}) {
			return nil, catalog.Schedule{}, ErrUnpriced
		}
	}
	converted, err := convertedCosts(costs, currency)
	if err != nil {
		return nil, catalog.Schedule{}, err
	}
	envelope, err := envelopeOf(converted)
	if err != nil {
		return nil, catalog.Schedule{}, err
	}
	return converted, envelope, nil
}

func priceFromRules(rules []Rule, m catalog.Model, snap *catalog.Snapshot, converted EndpointPrices, costErr error, currency platform.Currency, rates []platform.ExchangeRate) (saleProposal, *Rule, catalog.ReferencePrice, error) {
	for _, r := range rules {
		if !r.Enabled || !pricesModel(r.Target, m) {
			continue
		}
		ref := saleReference(snap, m.ID, currency, r.Method)
		if r.Method.Kind == "cost_plus" && r.Constraints == (Constraints{}) {
			proposal, err := costPlusPrice(r, converted, costErr, currency)
			return proposal, &r, ref, err
		}
		s, err := Sell(r, ref, currency, rates...)
		if errors.Is(err, ErrUnpriced) {
			continue
		}
		if errors.Is(err, ErrRuleRefuses) {
			return saleProposal{}, &r, ref, &RuleRefusal{Rule: r.ID, Model: m.ID}
		}
		return saleProposal{requested: s}, &r, ref, err
	}
	return saleProposal{}, nil, catalog.ReferencePrice{}, ErrUnpriced
}

// costPlusPrice prices every converted endpoint from its own cost and returns the model-level envelope of the result.
func costPlusPrice(r Rule, converted EndpointPrices, costErr error, currency platform.Currency) (saleProposal, error) {
	if costErr != nil {
		return saleProposal{}, costErr
	}
	endpoints := make(EndpointPrices, len(converted))
	for endpoint, cost := range converted {
		price, err := costPrice(r.Method, cost, currency)
		if err != nil {
			return saleProposal{}, err
		}
		endpoints[endpoint] = price
	}
	requested, err := envelopeOf(endpoints)
	return saleProposal{requested: requested, endpoints: endpoints, costs: converted}, err
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

func (p *Publication) protectPrice(input saleProposal, envelope catalog.Schedule, costErr error, policy PricePolicy, defaults fallbacks, free bool) error {
	if input.endpoints != nil {
		return p.protectEndpointPrices(input, envelope, costErr, policy, defaults, free)
	}
	return p.protectSharedPrice(input, envelope, costErr, policy, defaults, free)
}

func (p *Publication) protectSharedPrice(input saleProposal, envelope catalog.Schedule, costErr error, policy PricePolicy, defaults fallbacks, free bool) error {
	input.requested = input.requested.Fill(defaults.perUse)
	if free && !input.requested.IsZero() {
		return ErrFreePrice
	}
	var err error
	p.Requested, err = input.requested.MarshalJSON()
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
	floor, err := envelope.Scaled(policy.FloorMultiplier, input.requested.Currency())
	if err != nil {
		return err
	}
	p.Floor, err = floor.MarshalJSON()
	if err != nil {
		return err
	}
	input.requested, err = input.requested.Aligned(floor, false)
	if err != nil {
		return err
	}
	if free && !input.requested.IsZero() {
		return ErrFreePrice
	}
	p.Schedule, err = input.requested.MarshalJSON()
	if err != nil {
		return err
	}
	p.Status = "pending"
	return nil
}

// endpointPriceFloors floors every requested endpoint price against its own converted cost and returns the prices and
// the floors they were checked against.
func endpointPriceFloors(requested, costs EndpointPrices, policy PricePolicy) (EndpointPrices, EndpointPrices, error) {
	prices := make(EndpointPrices, len(requested))
	floors := make(EndpointPrices, len(requested))
	for endpoint, schedule := range requested {
		cost, ok := costs[endpoint]
		if !ok {
			return nil, nil, ErrUnpriced
		}
		floor, err := cost.Scaled(policy.FloorMultiplier, schedule.Currency())
		if err != nil {
			return nil, nil, err
		}
		floors[endpoint] = floor
		price, err := schedule.Aligned(floor, false)
		if err != nil {
			return nil, nil, err
		}
		prices[endpoint] = price
	}
	return prices, floors, nil
}

// protectEndpointPrices floors each endpoint's own price against that endpoint's own converted cost and publishes the
// dearest of them as the model-level schedule. The model-level schedule is the envelope of the endpoint prices rather
// than the envelope cost scaled: a node that reads only it then charges exactly the most any endpoint charges, whatever
// markup, fixed markup, bands or service tiers the endpoints have.
//
//nolint:cyclop // the publication fields are written in the order a reader needs them, not in branches
func (p *Publication) protectEndpointPrices(input saleProposal, envelope catalog.Schedule, costErr error, policy PricePolicy, defaults fallbacks, free bool) error {
	requested := make(EndpointPrices, len(input.endpoints))
	for endpoint, schedule := range input.endpoints {
		requested[endpoint] = schedule.Fill(defaults.perUse)
	}
	modelRequested, err := envelopeOf(requested)
	if err != nil {
		return err
	}
	p.Requested, err = modelRequested.MarshalJSON()
	if err != nil {
		return err
	}
	if free && !modelRequested.IsZero() {
		return ErrFreePrice
	}
	if costErr != nil {
		return costErr
	}
	p.RequestedEndpoints = requested
	p.Costs = input.costs
	p.Cost, err = envelope.MarshalJSON()
	if err != nil {
		return err
	}
	modelFloor, err := envelope.Scaled(policy.FloorMultiplier, modelRequested.Currency())
	if err != nil {
		return err
	}
	p.Floor, err = modelFloor.MarshalJSON()
	if err != nil {
		return err
	}
	prices, floors, err := endpointPriceFloors(requested, input.costs, policy)
	if err != nil {
		return err
	}
	modelPrice, err := envelopeOf(prices)
	if err != nil {
		return err
	}
	if free && !modelPrice.IsZero() {
		return ErrFreePrice
	}
	p.Schedule, err = modelPrice.MarshalJSON()
	if err != nil {
		return err
	}
	p.Endpoints, p.Floors = prices, floors
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
