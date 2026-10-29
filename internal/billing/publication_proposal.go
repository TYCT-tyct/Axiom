package billing

import (
	"context"
	"errors"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func proposePrice(policy PricePolicy, rules []Rule, m catalog.Model, snap *catalog.Snapshot, currency platform.Currency, costs []PriceCost, defaults fallbacks) (Publication, error) {
	p := Publication{Model: m.ID, Status: "unpriced"}
	envelope, costErr := modelPriceCost(costs, m, currency)
	ref, _ := snap.ReferencePrice(m.ID, currency.String())
	input, matched, err := priceFromRules(rules, m, ref, envelope, costErr, currency)
	if matched != nil && matched.Method.SameForEveryModel() {
		ref.ID = uuid.Nil()
	}
	if err != nil {
		p.Problem = err.Error()
	}
	p.Basis, err = priceEvidence(policy, matched, ref.ID, costs)
	if err != nil {
		return p, err
	}
	if matched != nil && p.Problem == "" {
		if err := p.protectPrice(input, envelope, costErr, policy, defaults); err != nil {
			p.Problem = err.Error()
			p.Schedule = nil
			p.Floor = nil
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

func priceFromRules(rules []Rule, m catalog.Model, ref catalog.ReferencePrice, envelope catalog.Schedule, costErr error, currency platform.Currency) (catalog.Schedule, *Rule, error) {
	for _, r := range rules {
		if !r.Enabled || !pricesModel(r.Target, m) {
			continue
		}
		if r.Method.Kind == "cost_plus" && r.Constraints == (Constraints{}) {
			if costErr != nil {
				return catalog.Schedule{}, &r, costErr
			}
			s, err := costPrice(r.Method, envelope, currency)
			return s, &r, err
		}
		s, err := Sell(r, ref, currency)
		if errors.Is(err, ErrUnpriced) {
			continue
		}
		if errors.Is(err, ErrRuleRefuses) {
			return catalog.Schedule{}, &r, &RuleRefusal{Rule: r.ID, Model: m.ID}
		}
		return s, &r, err
	}
	return catalog.Schedule{}, nil, ErrUnpriced
}

func priceEvidence(policy PricePolicy, rule *Rule, reference uuid.UUID, costs []PriceCost) (jsonx.Raw, error) {
	return jsonx.MarshalCanonical(struct {
		Policy    PricePolicy `json:"policy"`
		Rule      *Rule       `json:"rule"`
		Reference uuid.UUID   `json:"reference"`
		Costs     []PriceCost `json:"costs"`
	}{policy, rule, reference, costs})
}

func (p *Publication) protectPrice(input, envelope catalog.Schedule, costErr error, policy PricePolicy, defaults fallbacks) error {
	input = input.Fill(defaults.perUse)
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
