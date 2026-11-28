package suppliers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// An aggregator's line (docs/domains/suppliers-direct.md「OpenRouter 的成本」) is costed at the highest price any of a
// model's providers has, times the line's ratio: a plan of basis ceiling, which bounds the cost rather than being it.
// An attempt costs what the aggregator says it charged, in its credits times the ratio (what a credit costs us); with no
// figure in the answer it costs the bound until the aggregator's record of the generation is read.

// ceilingBasis is cost_schedules.basis of such a plan.
const ceilingBasis = "ceiling"

// maxPriceItems are the items OpenRouter's provider.max_price can bound, by its field and the units a price of the
// field is in: tokens are priced per million there, a request per request (OpenRouter《Provider Routing》).
var maxPriceItems = [...]struct {
	field string
	item  catalog.Item
	per   int64
}{
	{"prompt", catalog.ItemInput, 1_000_000},
	{"completion", catalog.ItemOutput, 1_000_000},
	{"request", catalog.ItemRequest, 1},
}

// ceilingTerms is what a ceiling plan's endpoint calls with: the line's ratio, and the provider object whose max_price
// holds the aggregator to the plan's prices in its credits, the plan over the ratio, rounded up to a whole 10⁻¹⁵: a
// provider at exactly the ceiling is let in.
func ceilingTerms(plan catalog.Schedule, ratioText string) (*big.Rat, []byte, error) {
	ratio, ok := new(big.Rat).SetString(ratioText)
	if !ok || ratio.Sign() <= 0 {
		return nil, nil, fmt.Errorf("the line's ratio %q is not above 0", ratioText)
	}
	limits := map[string]string{}
	for _, m := range maxPriceItems {
		rate, priced := plan.BaseRate(m.item)
		if !priced {
			continue
		}
		credits := new(big.Rat).Quo(rate.Rat(), ratio)
		limit, err := platform.CeilExact(credits.Mul(credits, big.NewRat(m.per, 1)))
		if err != nil {
			return nil, nil, fmt.Errorf("the max_price of %s: %w", m.field, err)
		}
		limits[m.field] = string(limit.AppendDecimal(nil))
	}
	provider, err := jsonx.MarshalCanonical(map[string]any{"max_price": limits})
	return ratio, provider, err
}

// ceilingCost is an attempt's cost at a ceiling plan from what the aggregator reported. bound is the plan's cost of
// the usage, which carries the ratio. A figure that is the whole charge costs the figure times the ratio, and is known;
// a BYOK call's figure is only the aggregator's fee, and the provider bills the model apart, so it costs the fee and the
// bound together and is not known; no figure costs the bound, which is not known either. boundCredits is the bound in
// credits, before the ratio, which a figure is judged against.
func ceilingCost(bound platform.Exact, ratio *big.Rat, reported *reportedCost) (native, boundCredits platform.Exact, known bool, err error) {
	if boundCredits, err = platform.CeilExact(new(big.Rat).Quo(bound.Rat(), ratio)); err != nil {
		return platform.Exact{}, platform.Exact{}, false, err
	}
	if reported == nil {
		return bound, boundCredits, false, nil
	}
	charged, err := platform.CeilExact(new(big.Rat).Mul(reported.amount.Rat(), ratio))
	if err != nil {
		return platform.Exact{}, platform.Exact{}, false, err
	}
	if !reported.byok {
		return charged, boundCredits, true, nil
	}
	native, err = charged.Add(bound)
	return native, boundCredits, false, err
}

// ceilingFresh is how long a read ceiling prices an endpoint. The ceilings are read every hour, so 24 hours lets the
// aggregator's listing fail for a whole day before its endpoints leave routing; past that, a provider that joined or
// raised its price since may be above the ceiling in an item max_price cannot bound.
const ceilingFresh = 24 * time.Hour

// aggregator is the adapter of kind when it is an aggregator's.
func (a *Admin) aggregator(kind string) (adapter.Aggregator, bool) {
	ad, ok := a.registry.Get(adapter.Kind(kind))
	if !ok {
		return nil, false
	}
	ag, ok := ad.(adapter.Aggregator)
	return ag, ok && ag.Aggregates()
}

// aggregates says the type of kind is an aggregator's.
func (a *Admin) aggregates(kind string) bool {
	_, ok := a.aggregator(kind)
	return ok
}

// priceCeilingLine prices each endpoint of an aggregator's line at the highest ceiling of the names its calls are sent
// by, its own and each key's, times ratio. An endpoint one of whose names has no ceiling read within ceilingFresh has an
// unknown cost and leaves routing: a key that sends that name would otherwise be costed at another name's prices. A
// plan in force again at the same prices keeps reconciliation's verdict (SetProjectedEndpointCost). The plan prices the
// standard tier only: the aggregator lists no price per tier, and the endpoint serves no other (Routable.StandardOnly).
// One that leaves an item the model reports unpriced is no plan the model's price can be costed against, and its
// endpoint stays out too, rather than withdrawing the model's shared price for every supplier (billing's
// allowsSharedCosts). It reports how many endpoints it priced.
func (a *Admin) priceCeilingLine(ctx context.Context, tx pg.Tx, q *store.Queries, snap *catalog.Snapshot, supplier, group uuid.UUID, ratio *big.Rat) (int, error) {
	ceilings, err := readCeilings(ctx, tx, q, group, a.now().Add(-ceilingFresh))
	if err != nil {
		return 0, err
	}
	endpoints, err := q.EndpointCallNames(ctx, tx, group)
	if err != nil {
		return 0, fmt.Errorf("read the line's endpoints: %w", err)
	}
	priced := 0
	for _, e := range endpoints {
		model, known := snap.Model(catalog.ModelID(e.ModelID))
		highest, ok := highestOf(ceilings, append([]string{e.UpstreamName}, e.CallNames...))
		reports := catalog.Reportable{Items: model.Reportable().Items &^ defaultedItems} // at the standard tier
		if !ok || !known || highest.Missing(reports) != (catalog.Reportable{}) {
			if err := q.UnsetProjectedEndpointCost(ctx, tx, store.UnsetProjectedEndpointCostParams{ID: e.ID, Reason: new(string(syncer.CostUnknown))}); err != nil {
				return 0, fmt.Errorf("take %s out of routing: %w", e.UpstreamName, err)
			}
			continue
		}
		plan, err := ceilingPlan(highest, ratio)
		if err != nil {
			return 0, fmt.Errorf("price %s: %w", e.UpstreamName, err)
		}
		id, err := q.EnsureCostSchedule(ctx, tx, store.EnsureCostScheduleParams{
			SupplierID: supplier, EndpointID: e.ID, NativeUnit: "usd", Schedule: plan.schedule, Basis: ceilingBasis,
			Fingerprint: plan.fingerprint,
		})
		if err != nil {
			return 0, fmt.Errorf("price %s: %w", e.UpstreamName, err)
		}
		if err := q.SetProjectedEndpointCost(ctx, tx, store.SetProjectedEndpointCostParams{ID: e.ID, CostScheduleID: id}); err != nil {
			return 0, fmt.Errorf("price %s: %w", e.UpstreamName, err)
		}
		priced++
	}
	return priced, nil
}

// readCeilings is the line's ceilings read after since, by name.
func readCeilings(ctx context.Context, tx pg.Tx, q *store.Queries, group uuid.UUID, since time.Time) (map[string]catalog.Schedule, error) {
	rows, err := q.NameCeilings(ctx, tx, group)
	if err != nil {
		return nil, fmt.Errorf("read the line's ceilings: %w", err)
	}
	out := make(map[string]catalog.Schedule, len(rows))
	for _, r := range rows {
		if r.Ceiling == nil || r.CeilingReadAt == nil || !r.CeilingReadAt.After(since) {
			continue
		}
		s, err := catalog.ParseSchedule(r.Ceiling)
		if err != nil {
			return nil, fmt.Errorf("the ceiling of %s: %w", r.UpstreamName, err)
		}
		out[r.UpstreamName] = s
	}
	return out, nil
}

// defaultedItems are the per-use items billing prices at the site's default when a schedule has none (billing's
// perUseItems): a ceiling without them still prices everything a call reports.
const defaultedItems catalog.ItemSet = 1<<catalog.ItemWebSearch | 1<<catalog.ItemCodeExecution

// highestOf is each item's highest price over the ceilings of names, at the standard tier, the one tier the
// aggregator's endpoint serves; false when a name has none.
func highestOf(ceilings map[string]catalog.Schedule, names []string) (catalog.Schedule, bool) {
	rates, currency, ok := highestRates(ceilings, names)
	if !ok {
		return catalog.Schedule{}, false
	}
	b, err := jsonx.MarshalCanonical(map[string]any{"v": 1, "currency": currency.String(), "rates": rates})
	if err != nil {
		return catalog.Schedule{}, false
	}
	s, err := catalog.ParseSchedule(b)
	return s, err == nil
}

// highestRates is each item's highest standard price over the ceilings of names, by the item's name, and their
// currency; false when a name has none.
func highestRates(ceilings map[string]catalog.Schedule, names []string) (map[string]string, platform.Currency, bool) {
	highest := map[catalog.Item]platform.UnitPrice{}
	var currency platform.Currency
	for _, n := range names {
		s, ok := ceilings[n]
		if !ok {
			return nil, currency, false
		}
		currency = s.Currency()
		for it := range catalog.NumItems {
			p, priced := s.BaseRate(it)
			if was, seen := highest[it]; priced && (!seen || p.Cmp(was) > 0) {
				highest[it] = p
			}
		}
	}
	rates := make(map[string]string, len(highest))
	for it, p := range highest {
		rates[it.String()] = p.String()
	}
	return rates, currency, true
}

// ceilingPlan is the plan of a ceiling at ratio, by a fingerprint of the ceiling's own prices and the ratio: the same
// prices read again are the plan the endpoint has, and no new version.
func ceilingPlan(ceiling catalog.Schedule, ratio *big.Rat) (plannedCost, error) {
	b, err := ceiling.MarshalJSON()
	if err != nil {
		return plannedCost{}, err
	}
	sum := sha256.Sum256(b)
	fingerprint := "ceiling:" + hex.EncodeToString(sum[:])
	if ratio.Cmp(big.NewRat(1, 1)) != 0 {
		text := ratioText(ratio)
		scaled, err := adapter.ScaleSchedule(ceiling, adapter.Amount(text))
		if err != nil {
			return plannedCost{}, err
		}
		if b, err = scaled.MarshalJSON(); err != nil {
			return plannedCost{}, err
		}
		fingerprint += ":ratio:" + text
	}
	return plannedCost{schedule: b, fingerprint: fingerprint}, nil
}

// reportedCost is an aggregator's figure of an attempt's cost, in its credits; byok says it is only the fee.
type reportedCost struct {
	amount platform.Exact
	byok   bool
}
