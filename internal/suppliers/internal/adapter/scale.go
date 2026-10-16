package adapter

import (
	"fmt"
	"math/big"
	"strings"

	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

// ScaleSchedule is a schedule with every price multiplied by a group's ratio. For a system that shows no prices (sub2api)
// it makes the draft cost plan to offer a person to confirm: the official schedule times the group's multiplier. A draft is only a draft. The official price is not what
// the supplier pays its own upstream, so the endpoint stays out of routing until a person confirms or corrects each
// model's base price, and reconciliation takes it down again if the plan turns out too low.
//
// The result is a cost plan: its currency is "XXX", "not a currency", because its amounts are the supplier's own unit
// and billing converts them (docs/domains/billing.md「供应商成本」).
func ScaleSchedule(official catalog.Schedule, multiplier Amount) (catalog.Schedule, error) {
	m, ok := new(big.Rat).SetString(string(multiplier))
	if !ok || m.Sign() < 0 {
		return catalog.Schedule{}, fmt.Errorf("adapter: scale: %q is not a multiplier", multiplier)
	}
	raw, err := official.MarshalJSON()
	if err != nil {
		return catalog.Schedule{}, err
	}
	var doc map[string]any
	if err := jsonx.Unmarshal(raw, &doc); err != nil {
		return catalog.Schedule{}, err
	}
	if err := scale(doc, m); err != nil {
		return catalog.Schedule{}, err
	}
	doc["currency"] = "XXX"
	out, err := jsonx.MarshalCanonical(doc)
	if err != nil {
		return catalog.Schedule{}, err
	}
	return catalog.ParseSchedule(out)
}

// unitPlaces is the finest price a schedule holds (10⁻¹⁵ of a unit per billing unit). A scaled price is rounded up to
// it, because a cost plan that is too low is the mistake to avoid.
const unitPlaces = 15

// scale multiplies every price under a "rates" member, at any depth, by m.
func scale(node any, m *big.Rat) error {
	switch n := node.(type) {
	case map[string]any:
		return scaleMembers(n, m)
	case []any:
		for _, v := range n {
			if err := scale(v, m); err != nil {
				return err
			}
		}
	}
	return nil
}

func scaleMembers(members map[string]any, m *big.Rat) error {
	for k, v := range members {
		rates, isRates := v.(map[string]any)
		if k != "rates" || !isRates {
			if err := scale(v, m); err != nil {
				return err
			}
			continue
		}
		if err := scaleRates(rates, m); err != nil {
			return err
		}
	}
	return nil
}

func scaleRates(rates map[string]any, m *big.Rat) error {
	for item, p := range rates {
		text, ok := p.(string)
		if !ok {
			return fmt.Errorf("adapter: scale: price of %s is not text", item)
		}
		scaled, err := scalePrice(text, m)
		if err != nil {
			return err
		}
		rates[item] = scaled
	}
	return nil
}

func scalePrice(text string, m *big.Rat) (string, error) {
	p, ok := new(big.Rat).SetString(text)
	if !ok {
		return "", fmt.Errorf("adapter: scale: price %q is not a number", text)
	}
	p.Mul(p, m)
	// Round up at unitPlaces: floor(p × 10^k + 1 - ε) / 10^k, done in integers.
	scaleUp := new(big.Int).Exp(big.NewInt(10), big.NewInt(unitPlaces), nil)
	num := new(big.Int).Mul(p.Num(), scaleUp)
	q, rem := new(big.Int).QuoRem(num, p.Denom(), new(big.Int))
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	s := new(big.Rat).SetFrac(q, scaleUp).FloatString(unitPlaces)
	return strings.TrimRight(strings.TrimRight(s, "0"), "."), nil
}
