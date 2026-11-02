package billing

import (
	"fmt"
	"math/big"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
)

// PriceCost is one endpoint's immutable cost evidence from the same reading that builds routing.
type PriceCost struct {
	Endpoint     uuid.UUID             `json:"endpoint"`
	Model        catalog.ModelID       `json:"model"`
	Schedule     catalog.Schedule      `json:"schedule"`
	UnitValue    string                `json:"unit_value"`
	UnitValueID  uuid.UUID             `json:"unit_value_id"`
	Currency     platform.Currency     `json:"-"`
	CurrencyCode string                `json:"currency"`
	Exchange     platform.ExchangeRate `json:"exchange"`
}

// CostEnvelope expresses known native costs in the site's currency with one upward rounding after all factors.
func CostEnvelope(costs []PriceCost, currency platform.Currency) (catalog.Schedule, error) {
	converted, err := convertedCosts(costs, currency)
	if err != nil {
		return catalog.Schedule{}, err
	}
	return envelopeOf(converted)
}

// convertedCosts is each endpoint's native cost in the site's currency: the cost a per-endpoint sale price is computed
// from, and the map CostEnvelope takes the dearest of.
func convertedCosts(costs []PriceCost, currency platform.Currency) (EndpointPrices, error) {
	if len(costs) == 0 {
		return nil, ErrUnpriced
	}
	converted := make(EndpointPrices, len(costs))
	for _, c := range costs {
		schedule, err := c.converted(currency)
		if err != nil {
			return nil, err
		}
		converted[c.Endpoint] = schedule
	}
	return converted, nil
}

func costPrice(m Method, cost catalog.Schedule, currency platform.Currency) (catalog.Schedule, error) {
	var factor *big.Rat
	if m.MarkupRatio != nil {
		factor, _ = new(big.Rat).SetString(*m.MarkupRatio)
		factor.Add(factor, big.NewRat(1, 1))
	} else {
		margin, _ := new(big.Rat).SetString(*m.MarginRatio)
		factor = new(big.Rat).Inv(new(big.Rat).Sub(big.NewRat(1, 1), margin))
	}
	s, err := cost.Scaled(factor.RatString(), currency)
	if err != nil || len(m.FixedMarkup) == 0 {
		return s, err
	}
	fixed, err := catalog.ParseSchedule(fmt.Appendf(nil, `{"v":1,"currency":%q,"rates":%s}`, currency.String(), m.FixedMarkup))
	if err != nil {
		return catalog.Schedule{}, err
	}
	return s.AddMarkup(fixed)
}

func (c PriceCost) converted(currency platform.Currency) (catalog.Schedule, error) {
	if c.UnitValueID == uuid.Nil() {
		return catalog.Schedule{}, ErrUnpriced
	}
	value, err := platform.ParseUnitPrice(c.UnitValue)
	if err != nil || value.Cmp(platform.UnitPrice{}) <= 0 {
		return catalog.Schedule{}, ErrUnpriced
	}
	factor := value.Rat()
	if c.Currency != currency {
		rate, ok := c.Exchange.Between(c.Currency, currency)
		if !ok {
			return catalog.Schedule{}, ErrUnpriced
		}
		factor.Mul(factor, rate)
	}
	return c.Schedule.Scaled(factor.RatString(), currency)
}

// parsePriceCosts reads the cost evidence stored with a price version. PriceCost keeps the currency out of its ordinary
// JSON because its plan uses the supplier's own unit; set it from the code before converting. Schedule has a custom
// marshaler but no unmarshaler, so its evidence is kept raw and parsed here rather than decoded into the struct.
func parsePriceCosts(raw []jsonx.Raw) ([]PriceCost, error) {
	out := make([]PriceCost, 0, len(raw))
	for _, item := range raw {
		var wire struct {
			Endpoint     uuid.UUID             `json:"endpoint"`
			Model        catalog.ModelID       `json:"model"`
			Schedule     jsonx.Raw             `json:"schedule"`
			UnitValue    string                `json:"unit_value"`
			UnitValueID  uuid.UUID             `json:"unit_value_id"`
			CurrencyCode string                `json:"currency"`
			Exchange     platform.ExchangeRate `json:"exchange"`
		}
		if err := jsonx.Unmarshal(item, &wire); err != nil {
			return nil, err
		}
		schedule, err := catalog.ParseSchedule(wire.Schedule)
		if err != nil {
			return nil, err
		}
		c := PriceCost{
			Endpoint: wire.Endpoint, Model: wire.Model, Schedule: schedule, UnitValue: wire.UnitValue,
			UnitValueID: wire.UnitValueID, CurrencyCode: wire.CurrencyCode, Exchange: wire.Exchange,
		}
		if c.CurrencyCode != "" {
			currency, err := platform.ParseCurrency(c.CurrencyCode)
			if err != nil {
				return nil, err
			}
			c.Currency = currency
		}
		out = append(out, c)
	}
	return out, nil
}

// basisCosts reads the cost evidence a candidate or version stores in its basis.
func basisCosts(raw jsonx.Raw) ([]PriceCost, error) {
	var basis struct {
		Costs []jsonx.Raw `json:"costs"`
	}
	if err := jsonx.Unmarshal(raw, &basis); err != nil {
		return nil, err
	}
	return parsePriceCosts(basis.Costs)
}

// endpointCostsFromBasis recovers the endpoint membership of a version from its cost evidence. A missing or unreadable
// record returns nil; the confirmation gate then falls back to the version's own price map.
func endpointCostsFromBasis(raw jsonx.Raw, currency platform.Currency) EndpointPrices {
	costs, err := basisCosts(raw)
	if err != nil {
		return nil
	}
	converted, err := convertedCosts(costs, currency)
	if err != nil {
		return nil
	}
	return converted
}

// endpointFloorsFromBasis is a proposal's break-even price per endpoint, recovered from the same cost evidence that
// priced it. The decision reads it only when a separately priced version returns to one shared price.
func endpointFloorsFromBasis(raw jsonx.Raw, currency platform.Currency, multiplier string) (EndpointPrices, error) {
	costs, err := basisCosts(raw)
	if err != nil {
		return nil, err
	}
	converted, err := convertedCosts(costs, currency)
	if err != nil {
		return nil, err
	}
	return endpointFloorsFromConverted(converted, currency, multiplier)
}

// endpointFloorsFromConverted applies the site floor multiplier to costs already converted to the site currency.
func endpointFloorsFromConverted(costs EndpointPrices, currency platform.Currency, multiplier string) (EndpointPrices, error) {
	floors := make(EndpointPrices, len(costs))
	for endpoint, cost := range costs {
		floor, err := cost.Scaled(multiplier, currency)
		if err != nil {
			return nil, err
		}
		floors[endpoint] = floor
	}
	return floors, nil
}
