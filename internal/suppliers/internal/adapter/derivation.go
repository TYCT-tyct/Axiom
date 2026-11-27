package adapter

import (
	"fmt"
	"math/big"
	"strings"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
)

// FactorKind names one input a price of a cost plan is the product of: CostFactorKind of api/admin.yaml, less the unit
// value, which is the supplier's and not the plan's.
type FactorKind string

// The factors a plan is derived from.
const (
	FactorModelRatio      FactorKind = "model_ratio"
	FactorCompletionRatio FactorKind = "completion_ratio"
	FactorCacheRatio      FactorKind = "cache_ratio"
	FactorCacheWriteRatio FactorKind = "cache_write_ratio"
	FactorGroupRatio      FactorKind = "group_ratio"
	FactorBasePrice       FactorKind = "base_price"     // a price of the base a person confirmed
	FactorOfficialPrice   FactorKind = "official_price" // a direct or cloud type's price, which the line's ratio scales
)

// Factor is one input of a price, as exact decimal text of at most 15 decimals (the contract's Decimal).
type Factor struct {
	Kind  FactorKind `json:"kind"`
	Value string     `json:"value"`
}

// DerivedRate is the factors of one price of a plan, at its place in the schedule: a service tier, the base band (Above
// nil) or a band above a threshold, and an item.
type DerivedRate struct {
	ServiceTier string   `json:"service_tier"`
	Above       *int64   `json:"above,omitempty"`
	Item        string   `json:"item"`
	Factors     []Factor `json:"factors"`
}

// Derivation is what each price of a cost plan is the product of, in the order the supplier multiplies them, so that a
// page can write it out (docs/domains/suppliers.md「价目行」). It explains a plan and prices nothing: the schedule alone
// is what an attempt is charged. Invariant (Check): each rate's factors multiply out to the schedule's price at that
// rate, rounded up to a unit price once, the plan's own rounding.
type Derivation []DerivedRate

// Derive is the derivation of every rate of s from the factors of each, which factors gives as exact fractions; nil when
// a factor is not a decimal of at most 15 places, since a factor shown rounded would not multiply out.
func Derive(s catalog.Schedule, factors func(catalog.ScheduleRate) map[FactorKind]*big.Rat, order []FactorKind) Derivation {
	rates := s.View().Rates
	d := make(Derivation, 0, len(rates))
	for _, r := range rates {
		of := factors(r)
		dr := DerivedRate{ServiceTier: r.ServiceTier.String(), Item: r.Item.String(), Factors: make([]Factor, 0, len(of))}
		if !r.Base {
			dr.Above = &r.Above
		}
		for _, kind := range order {
			v, ok := of[kind]
			if !ok {
				continue
			}
			text, ok := decimalOf(v)
			if !ok {
				return nil
			}
			dr.Factors = append(dr.Factors, Factor{Kind: kind, Value: text})
		}
		d = append(d, dr)
	}
	return d
}

// ScaledDerivation is the derivation of ScaleSchedule(base, ratio): each price is the confirmed base's price at the same
// place times the group's ratio.
func ScaledDerivation(base catalog.Schedule, ratio Amount) Derivation {
	r, ok := new(big.Rat).SetString(string(ratio))
	if !ok {
		return nil
	}
	return Derive(base, func(rate catalog.ScheduleRate) map[FactorKind]*big.Rat {
		return map[FactorKind]*big.Rat{FactorBasePrice: rate.Price.Rat(), FactorGroupRatio: r}
	}, []FactorKind{FactorBasePrice, FactorGroupRatio})
}

// Check reports whether the derivation explains s: one entry for each rate of s, at the same place, whose factors
// multiply out to its price rounded up to a unit price.
func (d Derivation) Check(s catalog.Schedule) error {
	rates := s.View().Rates
	if len(d) != len(rates) {
		return fmt.Errorf("adapter: a derivation of %d rates for a schedule of %d", len(d), len(rates))
	}
	for i, r := range rates {
		dr := d[i]
		if dr.ServiceTier != r.ServiceTier.String() || dr.Item != r.Item.String() || (dr.Above == nil) != r.Base ||
			dr.Above != nil && *dr.Above != r.Above {
			return fmt.Errorf("adapter: the derivation's rate %d is not the schedule's", i)
		}
		product, err := dr.Product()
		if err != nil {
			return err
		}
		if got, err := one.Scale(product); err != nil || got != r.Price {
			return fmt.Errorf("adapter: the factors of %s multiply out to %s, not %s", dr.Item, product.FloatString(18), r.Price)
		}
	}
	return nil
}

// Product is the exact product of the rate's factors.
func (dr DerivedRate) Product() (*big.Rat, error) {
	product := big.NewRat(1, 1)
	for _, f := range dr.Factors {
		v, ok := new(big.Rat).SetString(f.Value)
		if !ok {
			return nil, fmt.Errorf("adapter: the factor %s %q is not a number", f.Kind, f.Value)
		}
		product.Mul(product, v)
	}
	return product, nil
}

// Of is the factors of the rate at a place of the schedule; nil when the derivation has none for it.
func (d Derivation) Of(r catalog.ScheduleRate) []Factor {
	for _, dr := range d {
		if dr.ServiceTier == r.ServiceTier.String() && dr.Item == r.Item.String() &&
			(dr.Above == nil && r.Base || dr.Above != nil && !r.Base && *dr.Above == r.Above) {
			return dr.Factors
		}
	}
	return nil
}

// Text is the derivation as canonical JSON, the form a plan stores it in and fingerprints it by; "" for none.
func (d Derivation) Text() (string, error) {
	if d == nil {
		return "", nil
	}
	b, err := jsonx.MarshalCanonical(d)
	return string(b), err
}

// ParseDerivation reads a stored derivation; "" is none.
func ParseDerivation(text string) (Derivation, error) {
	if text == "" {
		return nil, nil
	}
	var d Derivation
	if err := jsonx.UnmarshalKnown([]byte(text), &d); err != nil {
		return nil, fmt.Errorf("adapter: a cost plan's derivation: %w", err)
	}
	return d, nil
}

// one is the unit price 1, which Scale rounds an exact product up to a unit price with.
var one = func() platform.UnitPrice {
	p, err := platform.ParseUnitPrice("1")
	if err != nil {
		panic(err)
	}
	return p
}()

// decimalOf is r as plain decimal text when it has at most 15 decimals and 18 whole digits, the contract's Decimal.
func decimalOf(r *big.Rat) (string, bool) {
	text := r.FloatString(15)
	if back, ok := new(big.Rat).SetString(text); !ok || back.Cmp(r) != 0 {
		return "", false
	}
	text = strings.TrimSuffix(strings.TrimRight(text, "0"), ".")
	whole, _, _ := strings.Cut(strings.TrimPrefix(text, "-"), ".")
	return text, len(whole) <= 18
}
