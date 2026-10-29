package billing

import (
	"context"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// PreviewPrices uses the publication calculation without saving a proposal or publishing a price. The rules may
// include an unsaved edit. The publication list, separately, shows the version actually served.
func PreviewPrices(ctx context.Context, tx pg.Tx, site uuid.UUID, currency platform.Currency, snap *catalog.Snapshot, rules []Rule, models []catalog.ModelID, costs []PriceCost) (map[catalog.ModelID]Publication, error) {
	policy, err := loadPricePolicy(ctx, tx, site)
	if err != nil {
		return nil, err
	}
	defaults, err := priceDefaults(ctx, tx, currency)
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
		p, err := proposePrice(policy, rules, m, snap, currency, byModel[id], defaults)
		if err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, nil
}
