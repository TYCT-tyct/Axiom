package adapter

import (
	"axiom/internal/catalog"
	"axiom/internal/platform"
)

// ScaleSchedule is a schedule with every price multiplied by a group's ratio. For a system that shows no prices
// (sub2api) it makes the draft cost plan to offer a person to confirm: the official schedule times the group's
// multiplier. A draft is only a draft. The official price is not what the supplier pays its own upstream, so the
// endpoint stays out of routing until a person confirms or corrects each model's base price, and reconciliation takes
// it down again if the plan turns out too low.
//
// The result is a cost plan: its currency is "XXX", "not a currency", because its amounts are the supplier's own unit
// and billing converts them (docs/domains/billing.md「供应商成本」).
func ScaleSchedule(official catalog.Schedule, multiplier Amount) (catalog.Schedule, error) {
	return official.Scaled(string(multiplier), platform.CurrencyNone)
}
