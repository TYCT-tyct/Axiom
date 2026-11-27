package suppliers

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// The line ratio (docs/domains/suppliers-direct.md「成本与线路倍率」). A direct or cloud line's cost is its type's price
// times the line's ratio, written into each endpoint's cost plan. Invariant: on a platform direct or cloud line, every
// priced endpoint's plan in force is the type's current price scaled by the line's ratio, each unit price rounded up
// once, and its factors multiply out to it (Derivation.Check); a change of the ratio and the endpoints' plans commit
// together. An account's own key (BYOK, also mode direct) is never priced here: its cost is zero.

var (
	// ErrCostRatio is a ratio that is not above 0 and at most 10 with at most 6 decimals.
	ErrCostRatio = errors.New("suppliers: a line's ratio is above 0 and at most 10, with at most 6 decimals")
	// ErrNotPasted is a line ratio set for a supplier whose ratio is not a person's to set: a relay's is read by its
	// sync.
	ErrNotPasted = errors.New("suppliers: only a direct or cloud supplier's line ratio is set by a person")
)

// costRatio is a ratio's form: a decimal of at most 6 places, checked for its range once parsed.
var costRatio = regexp.MustCompile(`^[0-9]{1,2}(\.[0-9]{1,6})?$`)

// readRatio checks a line ratio and gives it as the exact fraction it is and in its shortest text.
func readRatio(text string) (*big.Rat, string, error) {
	r, ok := new(big.Rat).SetString(text)
	if !costRatio.MatchString(text) || !ok || r.Sign() <= 0 || r.Cmp(big.NewRat(10, 1)) > 0 {
		return nil, "", ErrCostRatio
	}
	return r, ratioText(r), nil
}

// connectRatio is the ratio a line is connected at: the one given, or 1 when none is.
func connectRatio(text string) (*big.Rat, error) {
	if text == "" {
		return big.NewRat(1, 1), nil
	}
	r, _, err := readRatio(text)
	return r, err
}

// ratioText is the ratio's shortest decimal, as fingerprints and audits name it: 0.8, not 0.800000.
func ratioText(r *big.Rat) string {
	s := r.FloatString(6)
	for s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}

// SetCostRatio sets a platform direct or cloud supplier's line ratio and reprices every priced endpoint of the line at
// it, in the caller's transaction. snap is the catalog the prices come from.
func (a *Admin) SetCostRatio(ctx context.Context, tx pg.Tx, actor audit.Actor, snap *catalog.Snapshot, supplier uuid.UUID, ratio, reason string) (Supplier, error) {
	r, text, err := readRatio(ratio)
	if err != nil {
		return Supplier{}, err
	}
	s, err := a.view(ctx, tx, supplier, PlatformOwned)
	if err != nil {
		return Supplier{}, err
	}
	if s.Mode == relayMode {
		return Supplier{}, ErrNotPasted
	}
	if _, err := a.locked(ctx, tx, supplier); err != nil {
		return Supplier{}, err
	}
	q := store.New()
	groups, err := q.ListGroupsOf(ctx, tx, supplier)
	if err != nil || len(groups) != 1 {
		return Supplier{}, fmt.Errorf("read the line: %w (%d lines)", err, len(groups))
	}
	before := groups[0].Ratio
	if err := q.SetLineRatio(ctx, tx, store.SetLineRatioParams{ID: groups[0].ID, SupplierID: supplier, Ratio: text}); err != nil {
		return Supplier{}, fmt.Errorf("set the line's ratio: %w", err)
	}
	priced, err := a.priceLine(ctx, tx, snap, s, groups[0].ID, r)
	if err != nil {
		return Supplier{}, err
	}
	if err := q.BumpConfigGen(ctx, tx, supplier); err != nil {
		return Supplier{}, fmt.Errorf("raise the generation: %w", err)
	}
	if err := a.record(ctx, tx, actor, "supplier.cost_ratio", supplier, reason, map[string]any{
		"before": before, "after": text, "priced_endpoints": priced,
	}); err != nil {
		return Supplier{}, err
	}
	return a.view(ctx, tx, supplier, PlatformOwned)
}

// priceLine points each endpoint of a platform direct or cloud line whose model the type prices at the plan of the
// type's current price times ratio, the plan of that fingerprint when the endpoint has one already: plans are
// append-only and one row a fingerprint, so a ratio changed and changed back takes up the earlier plan again. An
// endpoint whose model the type does not price, or the catalog has no price for (withdrawn, say), has an unknown cost
// and leaves routing: the plan it had is a price that is no longer the line's. It reports how many endpoints it priced.
// s is a platform supplier, read with PlatformOwned: an account's own key (BYOK) is free, and its plans stay all zeros.
func (a *Admin) priceLine(ctx context.Context, tx pg.Tx, snap *catalog.Snapshot, s Supplier, group uuid.UUID, ratio *big.Rat) (int, error) {
	t, ok := a.TypeOf(s.Kind)
	if !ok {
		return 0, ErrNoType
	}
	currency := s.SettlementCurrency
	q := store.New()
	endpoints, err := q.LineEndpointsOf(ctx, tx, group)
	if err != nil {
		return 0, fmt.Errorf("read the line's endpoints: %w", err)
	}
	priced := 0
	for _, e := range endpoints {
		m, ok := snap.Model(catalog.ModelID(e.ModelID))
		ref, has := snap.ReferencePrice(catalog.ModelID(e.ModelID), currency)
		if !ok || !has || !(lineOffer{serves: t.Serves}).prices(m) {
			if err := q.UnsetProjectedEndpointCost(ctx, tx, store.UnsetProjectedEndpointCostParams{ID: e.ID, Reason: new(string(syncer.CostUnknown))}); err != nil {
				return 0, fmt.Errorf("take %s out of routing: %w", e.UpstreamName, err)
			}
			continue
		}
		if err := priceEndpoint(ctx, tx, q, s.ID, e.ID, ref, ratio); err != nil {
			return 0, fmt.Errorf("price %s: %w", e.UpstreamName, err)
		}
		priced++
	}
	return priced, nil
}

// priceEndpoint puts the plan of ref at ratio in force on the endpoint: the row of its fingerprint when the endpoint has
// one, a new row otherwise.
func priceEndpoint(ctx context.Context, tx pg.Tx, q *store.Queries, supplier, endpoint uuid.UUID, ref catalog.ReferencePrice, ratio *big.Rat) error {
	plan, err := linePlan(ref, ratio)
	if err != nil {
		return err
	}
	id, err := q.EnsureCostSchedule(ctx, tx, store.EnsureCostScheduleParams{
		SupplierID: supplier, EndpointID: endpoint, NativeUnit: "usd", Schedule: plan.schedule, Basis: "published",
		Fingerprint: plan.fingerprint, Derivation: plan.derivation,
	})
	if err != nil {
		return err
	}
	return q.SetEndpointCost(ctx, tx, store.SetEndpointCostParams{ID: endpoint, CostScheduleID: &id})
}

// plannedCost is a cost plan as the columns store it.
type plannedCost struct {
	schedule    []byte
	fingerprint string
	derivation  string // empty for none
}

// linePlan is the plan of a line at ratio: the reference price as it is at 1, without factors and by the fingerprint
// direct lines always had, so a line connected before ratios existed keeps its plan; otherwise every price times the
// ratio, with the factors it is the product of and the ratio in the fingerprint.
func linePlan(ref catalog.ReferencePrice, ratio *big.Rat) (plannedCost, error) {
	fingerprint := "price_list:" + ref.ID.String()
	schedule := ref.Schedule
	var derivation string
	if ratio.Cmp(big.NewRat(1, 1)) != 0 {
		text := ratioText(ratio)
		scaled, err := adapter.ScaleSchedule(ref.Schedule, adapter.Amount(text))
		if err != nil {
			return plannedCost{}, err
		}
		d := adapter.Derive(ref.Schedule, func(rate catalog.ScheduleRate) map[adapter.FactorKind]*big.Rat {
			return map[adapter.FactorKind]*big.Rat{adapter.FactorOfficialPrice: rate.Price.Rat(), adapter.FactorGroupRatio: ratio}
		}, []adapter.FactorKind{adapter.FactorOfficialPrice, adapter.FactorGroupRatio})
		if err := d.Check(scaled); err != nil {
			return plannedCost{}, err
		}
		b, err := jsonx.MarshalCanonical(d)
		if err != nil {
			return plannedCost{}, err
		}
		schedule, fingerprint, derivation = scaled, fingerprint+":ratio:"+text, string(b)
	}
	b, err := schedule.MarshalJSON()
	if err != nil {
		return plannedCost{}, err
	}
	return plannedCost{schedule: b, fingerprint: fingerprint, derivation: derivation}, nil
}

// locked takes the supplier's lock and reads the line's ratio under it, in the transaction that prices at it: a ratio
// read before the lock may be one setSupplierCostRatio has replaced since, and pricing at it would undo that change
// while the line shows the new ratio. It reports false for a supplier no longer active, which is left alone.
func (line *pastedLine) locked(ctx context.Context, tx pg.Tx, q *store.Queries) (bool, error) {
	if s, err := q.LockSupplier(ctx, tx, line.ID); err != nil || s.Status != "active" {
		return false, err
	}
	_, ratio, err := lineOf(ctx, tx, q, line.ID)
	line.ratio = ratio
	return err == nil, err
}
