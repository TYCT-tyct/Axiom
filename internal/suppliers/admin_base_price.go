package suppliers

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// A person's base price of a model a relay lists without a price (docs/domains/suppliers.md「管理操作」).
//
// Invariant: an attempt on an endpoint priced from a confirmed base is charged base × the group's ratio × the unit
// value. The base is the price in the settlement currency divided by the unit value in force when it is confirmed,
// rounded up to a unit price, so it is in the supplier's own unit; the sync scales it by the ratio it reads into the
// model's cost plan (rounded up once, as a published plan is); and Credits prices usage at that plan
// (TestConfirmedBaseIsWhatACallIsCharged). A price the supplier states always wins, and a base prices only the model it
// was confirmed for.

// The ways a base price is refused.
var (
	// ErrNoModelName is a group that lists no model of that upstream name.
	ErrNoModelName = errors.New("suppliers: the group lists no model of that name")
	// ErrNameNotMapped is an upstream name that maps to no model yet: a base prices a model, so the name is mapped
	// first.
	ErrNameNotMapped = errors.New("suppliers: the name maps to no model yet; map it first")
	// ErrModelChanged is an upstream name that maps to another model than the one the person confirmed it for: the
	// mapping changed since it was read, and the base would price a model nobody looked at.
	ErrModelChanged = errors.New("suppliers: the name maps to another model since it was read")
	// ErrNotUnpriced is a model the supplier states a price for, or one the sync has not read: a base stands in only for
	// a price the supplier does not state, as the state says when the base is confirmed.
	ErrNotUnpriced = errors.New("suppliers: the supplier states a price for the model, or the sync has not read it")
	// ErrNoUnitValue is a supplier whose unit value is not confirmed, which a base price is converted with.
	ErrNoUnitValue = errors.New("suppliers: confirm the supplier's unit value first")
	// ErrNoOfficialPrice is a model given no price that has no official price in the settlement currency to default to.
	ErrNoOfficialPrice = errors.New("suppliers: the model has no official price in the settlement currency; give one")
	// ErrBasePrice is a price that is not in the settlement currency, charges nothing, or is beyond a unit price in the
	// supplier's own unit.
	ErrBasePrice = errors.New("suppliers: a base price is in the settlement currency, charges something, and stays within a unit price in the supplier's unit")
)

// CostReason is why the cost of a model of a group is unknown, which says whether a base can price it: only an
// Unpriced one.
type CostReason string

// The reasons. The first three are the sync's (syncer.CostReason); UnderEstimated is reconciliation's.
const (
	// Unpriced is a model the supplier publishes no price for, and no base is confirmed for the model its name maps to.
	Unpriced CostReason = CostReason(syncer.Unpriced)
	// Unexpressible is a model the supplier publishes a price for that the price table cannot express.
	Unexpressible CostReason = CostReason(syncer.Unexpressible)
	// OverLimit is a confirmed base the group's ratio takes past the unit price limit.
	OverLimit CostReason = CostReason(syncer.OverLimit)
	// UnderEstimated is an endpoint reconciliation took down: the supplier charged more than its plan estimated.
	UnderEstimated CostReason = "under_estimated"
)

// costReasons tells why the costs of a supplier's models are unknown, from the sync's state and the endpoints' rows.
type costReasons struct {
	costs persist.Costs
}

func readCostReasons(ctx context.Context, tx pg.Tx, supplier uuid.UUID) (costReasons, error) {
	costs, err := persist.ReadCosts(ctx, tx, supplier)
	return costReasons{costs: costs}, err
}

// of is why the cost of the name in the group, for the model it maps to, is unknown; "" when it is not. state and
// stored are its endpoint's cost state and the reason the row keeps, nil without an endpoint.
// The state's unpriced comes first: an endpoint reconciliation took down whose supplier now states no price is one a
// base can price, which the endpoint's old verdict does not say.
func (r costReasons) of(groupRef, name string, model uuid.UUID, state, stored *string) CostReason {
	if state != nil && *state != "unknown" {
		return ""
	}
	why := CostReason(r.costs.Reason(groupRef, name, model))
	if why != Unpriced && stored != nil && *stored == string(UnderEstimated) {
		return UnderEstimated
	}
	return why
}

// MaxBasePrices is the most models one change names.
const MaxBasePrices = 500

// BaseModel is a model of a group of the supplier, by the upstream name the group lists it under.
type BaseModel struct {
	Group uuid.UUID
	Name  string
}

// BasePriceItem is one model to confirm. Price nil takes the model's official price in the catalog. Expect is the
// catalog model the person saw the name map to, nil when they did not say: a name that maps to another now is refused.
type BasePriceItem struct {
	BaseModel
	Price  *catalog.Schedule
	Expect *uuid.UUID
}

// BasePrice is a model's confirmed base, in the supplier's own unit: what the group's ratio scales.
type BasePrice struct {
	BaseModel
	Base catalog.Schedule
}

// OfficialPrices is where a default base comes from: *catalog.Snapshot.
type OfficialPrices interface {
	ReferencePrice(id catalog.ModelID, currency string) (catalog.ReferencePrice, bool)
}

// ConfirmBasePrices confirms the base price of each model, in the settlement currency, for the model its name maps to
// now; the next catalog round makes the model's cost plan of it at its group's ratio while the supplier states no price.
// It works before the model has an endpoint: the projection makes the endpoint with the plan. Confirming again replaces
// the base.
func (a *Admin) ConfirmBasePrices(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier uuid.UUID, items []BasePriceItem, official OfficialPrices, reason string) ([]BasePrice, error) {
	s, err := a.view(ctx, tx, supplier, PlatformOwned)
	if err != nil {
		return nil, err
	}
	perUnit, err := unitValueOf(s)
	if err != nil {
		return nil, err
	}
	// The state is locked before the names are read, so what a base is checked against is what it is saved over.
	costs, err := persist.LockCosts(ctx, tx, supplier, a.now())
	if err != nil {
		return nil, err
	}
	models := make([]BaseModel, len(items))
	for i, it := range items {
		models[i] = it.BaseModel
	}
	names, err := modelNames(ctx, tx, supplier, models)
	if err != nil {
		return nil, err
	}
	set, out, detail := make([]syncer.ConfirmedBase, len(items)), make([]BasePrice, len(items)), make([]map[string]any, len(items))
	for i, it := range items {
		n := names[it.BaseModel]
		if err := stillUnpriced(it, n, costs); err != nil {
			return nil, err
		}
		base, source, err := baseOf(it, n, s.SettlementCurrency, perUnit, official)
		if err != nil {
			return nil, err
		}
		text, err := base.MarshalJSON()
		if err != nil {
			return nil, err
		}
		set[i] = syncer.ConfirmedBase{GroupRef: n.UpstreamRef, Model: it.Name, For: n.ModelID.String(), Base: string(text)}
		out[i] = BasePrice{BaseModel: it.BaseModel, Base: base}
		detail[i] = map[string]any{
			"group": it.Group.String(), "upstream_name": it.Name, "model": n.ModelID.String(), "source": source,
			"unit_value": s.UnitValue, "base": string(text),
		}
	}
	if _, err := persist.ConfirmBases(ctx, tx, supplier, set, nil, a.now()); err != nil {
		return nil, err
	}
	if err := a.afterBases(ctx, tx, actor, supplier, "supplier.base_price_confirm", reason, detail); err != nil {
		return nil, err
	}
	return out, nil
}

// unitValueOf is what one unit of the supplier is worth in its settlement currency, which a base is converted with.
func unitValueOf(s Supplier) (*big.Rat, error) {
	if s.UnitValue == "" {
		return nil, ErrNoUnitValue
	}
	perUnit, ok := new(big.Rat).SetString(s.UnitValue)
	if !ok || perUnit.Sign() <= 0 {
		return nil, fmt.Errorf("suppliers: the unit value %q", s.UnitValue)
	}
	return perUnit, nil
}

// ClearBasePrices takes the confirmed base of each model away; at the next catalog round a model the supplier states no
// price for has its cost unknown again, whether or not its models are read. It answers the bases it took away; when
// there were none, nothing changes and nothing is recorded.
func (a *Admin) ClearBasePrices(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier uuid.UUID, models []BaseModel, reason string) ([]BasePrice, error) {
	if _, err := a.view(ctx, tx, supplier, PlatformOwned); err != nil {
		return nil, err
	}
	names, err := modelNames(ctx, tx, supplier, models)
	if err != nil {
		return nil, err
	}
	keys := make([]syncer.BaseKey, len(models))
	byKey := make(map[syncer.BaseKey]BaseModel, len(models))
	for i, m := range models {
		keys[i] = syncer.BaseKey{GroupRef: names[m].UpstreamRef, Model: m.Name}
		byKey[keys[i]] = m
	}
	cleared, err := persist.ConfirmBases(ctx, tx, supplier, nil, keys, a.now())
	if err != nil {
		return nil, err
	}
	out, detail := []BasePrice{}, []map[string]any{}
	for _, c := range cleared {
		base, err := catalog.ParseSchedule([]byte(c.Base))
		if err != nil {
			return nil, fmt.Errorf("the confirmed base of %s in %s: %w", c.Model, c.GroupRef, err)
		}
		m := byKey[c.BaseKey]
		out = append(out, BasePrice{BaseModel: m, Base: base})
		detail = append(detail, map[string]any{"group": m.Group.String(), "upstream_name": m.Name, "model": c.For, "base": c.Base})
	}
	if len(out) == 0 {
		if _, err := a.locked(ctx, tx, supplier); err != nil {
			return nil, err
		}
		return out, synced(ctx, tx, supplier)
	}
	if err := a.afterBases(ctx, tx, actor, supplier, "supplier.base_price_clear", reason, detail); err != nil {
		return nil, err
	}
	return out, nil
}

// afterBases locks the supplier, after its state as a step takes them, checks the sync runs for it, raises the
// generation and records the change.
func (a *Admin) afterBases(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier uuid.UUID, action, reason string, items []map[string]any) error {
	if _, err := a.locked(ctx, tx, supplier); err != nil {
		return err
	}
	if err := synced(ctx, tx, supplier); err != nil {
		return err
	}
	if err := store.New().BumpConfigGen(ctx, tx, supplier); err != nil {
		return fmt.Errorf("raise the generation: %w", err)
	}
	return a.record(ctx, tx, actor, action, supplier, reason, map[string]any{"items": items})
}

// stillUnpriced checks an item against the name's mapping now, aliases at their current revision included, and the locked
// state: the model the person saw, and a price the supplier still does not state.
func stillUnpriced(it BasePriceItem, n store.ModelNamesOfRow, costs persist.Costs) error {
	switch {
	case it.Expect != nil && (n.ModelID == nil || *n.ModelID != *it.Expect):
		return fmt.Errorf("%w: %s", ErrModelChanged, it.Name)
	case n.ModelID == nil:
		return fmt.Errorf("%w: %s", ErrNameNotMapped, it.Name)
	case !costs.Unpublished(n.UpstreamRef, it.Name):
		return fmt.Errorf("%w: %s", ErrNotUnpriced, it.Name)
	}
	return nil
}

// baseOf is the base of one item in the supplier's own unit, and where its price came from: "entered" for the item's,
// or the source of the catalog's reference price of the model its name maps to.
func baseOf(it BasePriceItem, n store.ModelNamesOfRow, currency string, perUnit *big.Rat, official OfficialPrices) (catalog.Schedule, string, error) {
	price, source := it.Price, "entered"
	if price == nil {
		ref, ok := official.ReferencePrice(catalog.ModelID(*n.ModelID), currency)
		if !ok {
			return catalog.Schedule{}, "", fmt.Errorf("%w: %s", ErrNoOfficialPrice, it.Name)
		}
		price, source = &ref.Schedule, string(ref.Source)
	}
	base, err := nativeBase(*price, currency, perUnit)
	if err != nil {
		return catalog.Schedule{}, "", fmt.Errorf("%w: %s", err, it.Name)
	}
	return base, source, nil
}

// modelNames reads the models the supplier's groups list under these names; ErrNoModelName names one they do not.
func modelNames(ctx context.Context, tx pg.Tx, supplier uuid.UUID, models []BaseModel) (map[BaseModel]store.ModelNamesOfRow, error) {
	params := store.ModelNamesOfParams{SupplierID: supplier, Groups: make([]uuid.UUID, len(models)), Names: make([]string, len(models))}
	for i, m := range models {
		params.Groups[i], params.Names[i] = m.Group, m.Name
	}
	rows, err := store.New().ModelNamesOf(ctx, tx, params)
	if err != nil {
		return nil, fmt.Errorf("read the models: %w", err)
	}
	found := make(map[BaseModel]store.ModelNamesOfRow, len(rows))
	for _, r := range rows {
		found[BaseModel{Group: r.SupplierGroupID, Name: r.UpstreamName}] = r
	}
	for _, m := range models {
		if _, ok := found[m]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrNoModelName, m.Name)
		}
	}
	return found, nil
}

// nativeBase is a price in the settlement currency in the supplier's own unit: divided by what one unit is worth,
// rounded up to a unit price, so the base never costs a call at less than the price.
func nativeBase(price catalog.Schedule, currency string, perUnit *big.Rat) (catalog.Schedule, error) {
	if price.Currency().String() != currency || price.IsZero() {
		return catalog.Schedule{}, ErrBasePrice
	}
	base, err := price.Scaled(new(big.Rat).Inv(perUnit).RatString(), platform.CurrencyNone)
	if err != nil {
		return catalog.Schedule{}, errors.Join(ErrBasePrice, err)
	}
	return base, nil
}
