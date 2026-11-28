package suppliers

import (
	"context"
	"fmt"
	"math/big"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/store"
)

// Invariant (docs/domains/suppliers.md「决策与理由」): a price line shows what an attempt is charged. Its native price is
// the cost plan's own rate at that condition, the rate Service.Cost prices usage at, and its price is that rate in the
// settlement currency by inSettlement, the conversion Credits makes, rounded up to a unit price only for the contract's
// 15 decimals. An attempt of n units at the line's condition is charged n × native price × unit value, rounded up to a
// billionth once (TestEndpointCostIsWhatAnAttemptIsCharged).

// inSettlement is a native amount in the settlement currency at a unit value, exactly: the conversion Credits makes
// before it rounds once, and the one each price line of EndpointCost is converted with.
func inSettlement(native, unitValue *big.Rat) *big.Rat { return new(big.Rat).Mul(native, unitValue) }

// CostLine is one price of an endpoint's cost plan: an item at a service tier, in the base band or above a threshold.
type CostLine struct {
	Item        catalog.Item
	ServiceTier catalog.ServiceTier
	Base        bool  // the base band, which no threshold has to be passed for
	Above       int64 // the threshold of a band above the base
	Native      platform.UnitPrice
	// Price is Native in the settlement currency, rounded up to a unit price; nil while no unit value is confirmed or
	// when it is beyond what a unit price may be.
	Price *platform.UnitPrice
	// Factors are what Native is the product of, in order, rounded up to a unit price once: a relay's ratios, or a
	// confirmed base price and the group's ratio. Empty for a plan stored without them (one from before plans kept
	// them, or one whose adapter gives none). The unit value Price adds is the supplier's, not the plan's, and not here.
	Factors []CostFactor
}

// CostFactor is one input of a price line (CostFactorKind of api/admin.yaml), as exact decimal text.
type CostFactor struct {
	Kind  string
	Value string
}

// EndpointCost is an endpoint's cost plan in force as price lines. Plan is nil, and Lines empty, while the cost is not
// known.
type EndpointCost struct {
	Endpoint  uuid.UUID
	Model     catalog.ModelID
	CostState string
	Plan      *uuid.UUID
	// Basis is where the plan came from: "published", the supplier's own prices as the sync read them, "confirmed",
	// a base price a person confirmed times the group's ratio, or "ceiling", an aggregator's highest price times the
	// line's ratio, which bounds what an attempt costs (aggregator.go).
	Basis              string
	NativeUnit         string
	SettlementCurrency string
	UnitValue          *platform.UnitPrice // nil while none is confirmed
	Schedule           *catalog.Schedule
	Lines              []CostLine
}

// EndpointCost reads an endpoint of the supplier of the owners with its cost plan as price lines; ErrNoSupplier or
// ErrNoSupplierEndpoint when there is none.
func (a *Admin) EndpointCost(ctx context.Context, tx pg.Tx, supplier, endpoint uuid.UUID, owners Owners) (EndpointCost, error) {
	s, err := a.view(ctx, tx, supplier, owners)
	if err != nil {
		return EndpointCost{}, err
	}
	rows, err := store.New().GetEndpointPlan(ctx, tx, store.GetEndpointPlanParams{ID: endpoint, SupplierID: supplier})
	if err != nil {
		return EndpointCost{}, fmt.Errorf("read the endpoint's plan: %w", err)
	}
	if len(rows) == 0 {
		return EndpointCost{}, ErrNoSupplierEndpoint
	}
	r := rows[0]
	c := EndpointCost{
		Endpoint: r.ID, Model: catalog.ModelID(r.ModelID), CostState: r.CostState, NativeUnit: s.NativeUnit,
		SettlementCurrency: s.SettlementCurrency,
	}
	if s.UnitValue != "" {
		uv, err := platform.ParseUnitPrice(s.UnitValue)
		if err != nil {
			return EndpointCost{}, fmt.Errorf("the unit value %q: %w", s.UnitValue, err)
		}
		c.UnitValue = &uv
	}
	if r.CostState != "known" || r.PlanID == nil {
		return c, nil
	}
	schedule, err := catalog.ParseSchedule(r.Schedule)
	if err != nil {
		return EndpointCost{}, fmt.Errorf("the cost plan %s: %w", *r.PlanID, err)
	}
	derivation, err := adapter.ParseDerivation(r.Derivation)
	if err != nil {
		return EndpointCost{}, fmt.Errorf("the cost plan %s: %w", *r.PlanID, err)
	}
	c.Plan, c.Basis, c.Schedule = r.PlanID, deref(r.Basis), &schedule
	for _, rate := range schedule.View().Rates {
		c.Lines = append(c.Lines, costLine(rate, derivation.Of(rate), c.UnitValue))
	}
	return c, nil
}

// costLine is one rate of a plan as a price line: in the settlement currency when a unit value is confirmed.
func costLine(rate catalog.ScheduleRate, factors []adapter.Factor, unitValue *platform.UnitPrice) CostLine {
	line := CostLine{Item: rate.Item, ServiceTier: rate.ServiceTier, Base: rate.Base, Above: rate.Above, Native: rate.Price}
	for _, f := range factors {
		line.Factors = append(line.Factors, CostFactor{Kind: string(f.Kind), Value: f.Value})
	}
	if unitValue != nil {
		if price, err := oneUnit.Scale(inSettlement(rate.Price.Rat(), unitValue.Rat())); err == nil {
			line.Price = &price
		}
	}
	return line
}

// oneUnit is 1, which Scale rounds an exact amount up to a unit price with.
var oneUnit = func() platform.UnitPrice {
	p, err := platform.ParseUnitPrice("1")
	if err != nil {
		panic(err)
	}
	return p
}()

// Traffic is what an endpoint answered in the last 24 hours by the database's clock: how many attempts, and the units
// of each item they were priced at.
type Traffic struct {
	Requests int64
	Units    map[catalog.Item]int64
}

// EndpointTraffic reads an endpoint's traffic of the last 24 hours, test generations left out.
func (*Admin) EndpointTraffic(ctx context.Context, tx pg.Tx, endpoint uuid.UUID) (Traffic, error) {
	requests, units, err := store.EndpointTraffic(ctx, tx, endpoint)
	if err != nil {
		return Traffic{}, fmt.Errorf("read the endpoint's traffic: %w", err)
	}
	t := Traffic{Requests: requests, Units: map[catalog.Item]int64{}}
	for name, n := range units {
		if it, err := catalog.ParseItem(name); err == nil {
			t.Units[it] = n
		}
	}
	return t, nil
}
