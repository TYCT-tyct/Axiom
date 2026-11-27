package billing

import (
	"context"
	"slices"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// PreviewPrices uses the publication calculation without saving a proposal or publishing a price. The rules may
// include an unsaved edit. The publication list, separately, shows the version actually served.
func PreviewPrices(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency, snap *catalog.Snapshot, rules []Rule, models []catalog.ModelID, costs []PriceCost, rates ...platform.ExchangeRate) (map[catalog.ModelID]Publication, error) {
	policy, err := loadPricePolicy(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	defaults, err := priceDefaults(ctx, tx, currency)
	if err != nil {
		return nil, err
	}
	rows, err := queries.ReadPublications(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	bases, err := publishedBases(rows, policy)
	if err != nil {
		return nil, err
	}
	byModel := map[catalog.ModelID][]PriceCost{}
	for _, c := range costs {
		byModel[c.Model] = append(byModel[c.Model], c)
	}
	out := map[catalog.ModelID]Publication{}
	for _, id := range models {
		m, ok := snap.Model(id)
		if !ok {
			continue
		}
		var base *scaledBase
		if b, ok := bases[id]; ok {
			base = &b
		}
		p, err := previewPrice(policy, rules, base, m, snap, currency, byModel[id], defaults, rates...)
		if err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, nil
}

// previewPrice is what the one account or tier the rules are for pays, chosen as serving chooses it
// (AudiencePrices.Select): each audience's own rules make its layer, as publication makes them, and the layer whose
// price came from the most specific target wins, the account's before the tier's before everyone's. Everyone's layer
// is the answer when no layer has a price. A default multiplier scales the published price for all, as serving does.
func previewPrice(policy PricePolicy, rules []Rule, base *scaledBase, m catalog.Model, snap *catalog.Snapshot, currency platform.Currency, costs []PriceCost, defaults fallbacks, rates ...platform.ExchangeRate) (Publication, error) {
	layers := make([][]Rule, len(audienceKinds))
	for _, r := range rules {
		i := slices.Index(audienceKinds, r.Audience.Kind)
		layers[i] = append(layers[i], r)
	}
	var best Publication
	specificity := len(targetKinds)
	for i, layer := range layers {
		if len(layer) == 0 && i < len(layers)-1 {
			continue
		}
		p, err := proposePrice(policy, layer, base, m, snap, currency, costs, defaults, rates...)
		if err != nil {
			return Publication{}, err
		}
		if target := slices.Index(targetKinds, p.target.Kind); p.matched && target < specificity {
			best, specificity = p, target
		}
		if i == len(layers)-1 && specificity == len(targetKinds) {
			best = p
		}
	}
	return best, nil
}
