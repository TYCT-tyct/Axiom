package suppliers

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/persist"
	"axiom/internal/suppliers/internal/store"
	"axiom/internal/suppliers/internal/syncer"
)

// A person's base price of an endpoint whose supplier publishes none (docs/domains/suppliers.md「确认基础价」).
//
// Invariant: an attempt on an endpoint priced from a confirmed base is charged base × the group's ratio × the unit
// value: the base is stored in the supplier's own unit, the sync scales it by the ratio it reads into the endpoint's
// cost plan (rounded up once, as a published plan is), and Credits prices usage at that plan
// (TestConfirmedBaseIsWhatACallIsCharged). A plan the supplier publishes always wins over a confirmed base.

// The ways a base price is refused.
var (
	// ErrNoOfficialPrice is an endpoint given no price whose model has no official price in US dollars to default to.
	ErrNoOfficialPrice = errors.New("suppliers: the model has no official price in USD to confirm; give one")
	// ErrBasePrice is a price that is not in US dollars, charges nothing, or is beyond a unit price in the supplier's
	// own unit.
	ErrBasePrice = errors.New("suppliers: a base price is a schedule in USD that charges something, within a unit price in the supplier's unit")
)

// MaxBasePrices is the most endpoints one change names.
const MaxBasePrices = 500

// unitsPerDollar is how many of a supplier's own units a US dollar of its prices is: sub2api, CPA and a declared
// relay count dollars; new-api and one-api count quota, whose ratio 1 is $0.002 per 1K tokens, 500,000 quota to the
// dollar whatever the site's quota_per_unit (that only says what it sells a dollar of balance for).
var unitsPerDollar = map[string]string{"usd": "1", "quota": "500000"}

// BasePriceItem is one endpoint to confirm. Price nil takes the model's official price in the catalog.
type BasePriceItem struct {
	Endpoint uuid.UUID
	Price    *catalog.Schedule
}

// BasePrice is an endpoint's confirmed base, in the supplier's own unit: what the group's ratio scales.
type BasePrice struct {
	Endpoint uuid.UUID
	Base     catalog.Schedule
}

// OfficialPrices is where a default base comes from: *catalog.Snapshot.
type OfficialPrices interface {
	ReferencePrice(id catalog.ModelID, currency string) (catalog.ReferencePrice, bool)
}

// ConfirmBasePrices confirms the base price of each endpoint, in USD, as the supplier's own unit; the next catalog round
// makes each endpoint's cost plan of it at its group's ratio, unless the supplier publishes one. Confirming again
// replaces the base.
func (a *Admin) ConfirmBasePrices(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier uuid.UUID, items []BasePriceItem, official OfficialPrices, reason string) ([]BasePrice, error) {
	s, err := a.view(ctx, tx, supplier, PlatformOwned)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(items))
	for i, it := range items {
		ids[i] = it.Endpoint
	}
	endpoints, err := endpointKeys(ctx, tx, supplier, ids)
	if err != nil {
		return nil, err
	}
	set, out, detail := map[string]string{}, make([]BasePrice, len(items)), make([]map[string]any, len(items))
	for i, it := range items {
		e := endpoints[it.Endpoint]
		price, source := it.Price, "entered"
		if price == nil {
			ref, ok := official.ReferencePrice(catalog.ModelID(e.ModelID), "USD")
			if !ok {
				return nil, fmt.Errorf("%w: endpoint %s", ErrNoOfficialPrice, it.Endpoint)
			}
			price, source = &ref.Schedule, "official"
		}
		base, err := nativeBase(*price, s.NativeUnit)
		if err != nil {
			return nil, fmt.Errorf("%w: endpoint %s", err, it.Endpoint)
		}
		text, err := base.MarshalJSON()
		if err != nil {
			return nil, err
		}
		key := syncer.PlanKey(e.UpstreamRef, e.UpstreamName)
		set[key] = string(text)
		out[i] = BasePrice{Endpoint: it.Endpoint, Base: base}
		detail[i] = map[string]any{"endpoint": it.Endpoint.String(), "plan_key": key, "source": source, "base": string(text)}
	}
	if _, err := persist.ConfirmBases(ctx, tx, supplier, set, nil, a.now()); err != nil {
		return nil, err
	}
	if err := a.afterBases(ctx, tx, actor, supplier, "supplier.base_price_confirm", reason, detail); err != nil {
		return nil, err
	}
	return out, nil
}

// ClearBasePrices takes the confirmed base of each endpoint away; the next catalog round leaves an endpoint the
// supplier publishes no price for with its cost unknown. It answers the bases it took away.
func (a *Admin) ClearBasePrices(ctx context.Context, tx pg.Tx, actor audit.Actor, supplier uuid.UUID, ids []uuid.UUID, reason string) ([]BasePrice, error) {
	if _, err := a.view(ctx, tx, supplier, PlatformOwned); err != nil {
		return nil, err
	}
	endpoints, err := endpointKeys(ctx, tx, supplier, ids)
	if err != nil {
		return nil, err
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = syncer.PlanKey(endpoints[id].UpstreamRef, endpoints[id].UpstreamName)
	}
	cleared, err := persist.ConfirmBases(ctx, tx, supplier, nil, keys, a.now())
	if err != nil {
		return nil, err
	}
	out, detail := []BasePrice{}, []map[string]any{}
	for i, id := range ids {
		text, ok := cleared[keys[i]]
		if !ok {
			continue
		}
		base, err := catalog.ParseSchedule([]byte(text))
		if err != nil {
			return nil, fmt.Errorf("the confirmed base of %s: %w", keys[i], err)
		}
		delete(cleared, keys[i]) // an endpoint named twice is answered once
		out = append(out, BasePrice{Endpoint: id, Base: base})
		detail = append(detail, map[string]any{"endpoint": id.String(), "plan_key": keys[i], "base": text})
	}
	if len(out) == 0 { // nothing to take away changes nothing and records nothing
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

// endpointKeys reads the endpoints of the supplier by id; ErrNoSupplierEndpoint names one it does not have.
func endpointKeys(ctx context.Context, tx pg.Tx, supplier uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]store.EndpointKeysOfRow, error) {
	rows, err := store.New().EndpointKeysOf(ctx, tx, store.EndpointKeysOfParams{SupplierID: supplier, Ids: ids})
	if err != nil {
		return nil, fmt.Errorf("read the endpoints: %w", err)
	}
	found := make(map[uuid.UUID]store.EndpointKeysOfRow, len(rows))
	for _, r := range rows {
		found[r.ID] = r
	}
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrNoSupplierEndpoint, id)
		}
	}
	return found, nil
}

// nativeBase is a price in US dollars in the supplier's own unit.
func nativeBase(price catalog.Schedule, nativeUnit string) (catalog.Schedule, error) {
	per, ok := unitsPerDollar[nativeUnit]
	if !ok {
		return catalog.Schedule{}, fmt.Errorf("suppliers: a native unit %q", nativeUnit)
	}
	if price.Currency().String() != "USD" || price.IsZero() {
		return catalog.Schedule{}, ErrBasePrice
	}
	base, err := price.Scaled(per, platform.CurrencyNone)
	if err != nil {
		return catalog.Schedule{}, errors.Join(ErrBasePrice, err)
	}
	return base, nil
}
