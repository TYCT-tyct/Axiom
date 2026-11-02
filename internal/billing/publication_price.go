package billing

import (
	"fmt"
	"math/big"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
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
