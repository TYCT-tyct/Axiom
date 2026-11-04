package billing

import (
	"fmt"
	"math/big"
	"slices"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
)

// Check reports the first part of the rule that breaks a rule of rules. currency is the site's: a fixed price is in it.
func (in RuleInput) Check(currency platform.Currency) error {
	if err := checkTarget(in.Target); err != nil {
		return err
	}
	if err := checkAudience(in.Audience); err != nil {
		return err
	}
	if err := in.Method.check(currency); err != nil {
		return err
	}
	return in.Constraints.check()
}

// CheckItems applies the catalog's new-item switches to the price lists a rule carries. Check validates their form;
// this refuses a price on an item a node running an older release cannot read, whether the rule sells it as a whole
// price list or marks it up by a fixed amount, so a stored rule cannot make another node's price refresh fail.
func (in RuleInput) CheckItems(allow func(catalog.Schedule) error) error {
	if allow == nil {
		return nil
	}
	return in.Method.checkItems(allow)
}

func (m Method) checkItems(allow func(catalog.Schedule) error) error {
	if len(m.Schedule) > 0 {
		s, err := catalog.ParseSchedule(m.Schedule)
		if err != nil {
			return &RuleError{"method/schedule", "is not a price list of the catalog's form"}
		}
		if err := allow(s); err != nil {
			return &RuleError{"method/schedule", err.Error()}
		}
	}
	if len(m.FixedMarkup) > 0 {
		s, err := catalog.ParseSchedule(fmt.Appendf(nil, `{"v":1,"currency":"XXX","rates":%s}`, m.FixedMarkup))
		if err != nil {
			return &RuleError{"method/fixed_markup", "is not a set of prices"}
		}
		if err := allow(s); err != nil {
			return &RuleError{"method/fixed_markup", err.Error()}
		}
	}
	return nil
}

func checkTarget(t Selector) error {
	if !slices.Contains(targetKinds, t.Kind) {
		return &RuleError{"target/kind", "is not a thing a rule prices"}
	}
	var valid bool
	switch t.Kind {
	case "all":
		valid = t.ID == ""
	case "author":
		valid = authorSlug.MatchString(t.ID)
	case "source_type":
		valid = sourceType.MatchString(t.ID)
	default:
		id, err := uuid.Parse(t.ID)
		valid = err == nil && id.String() == t.ID
	}
	if !valid {
		return &RuleError{"target/id", "is not the ID of a " + t.Kind + ", and is absent only for all"}
	}
	if !slices.Contains(honouredTargets, t.Kind) {
		return &RuleError{"target/kind", "can only be a model, an author or all in this release: a rule for an endpoint, group, supplier or source type would be accepted and then not applied"}
	}
	return nil
}

func checkAudience(a Selector) error {
	switch a.Kind {
	case "all":
		if a.ID != "" {
			return &RuleError{"audience/id", "is absent for all"}
		}
		return nil
	case "account":
		id, err := accounts.ParseAccountID(a.ID)
		if err == nil && id != (accounts.AccountID{}) {
			return nil
		}
	case "tier":
		id, err := accounts.ParseTierID(a.ID)
		if err == nil && id != (accounts.TierID{}) {
			return nil
		}
	default:
		return &RuleError{"audience/kind", "is not an audience a rule prices for"}
	}
	return &RuleError{"audience/id", "is not a nonzero ID of the audience kind"}
}

// ratio reads a decimal member; ok is false for an absent one.
func ratio(field string, v *string) (r *big.Rat, ok bool, err error) {
	if v == nil {
		return nil, false, nil
	}
	if !decimal.MatchString(*v) {
		return nil, false, &RuleError{field, "is not a plain decimal that is not negative"}
	}
	r, _ = new(big.Rat).SetString(*v) // the pattern is a decimal
	return r, true, nil
}

// check accepts only the methods the publication calculation executes. Exchange-based reference selection is still
// unavailable, so it is refused at save instead of silently leaving a model to a broader rule.
func (m Method) check(currency platform.Currency) error {
	switch m.Kind {
	case "cost_plus":
		if err := m.checkCostPlus(); err != nil {
			return err
		}
		return nil
	case "reference_multiplier":
		if err := m.checkMultiplier(); err != nil {
			return err
		}
		if m.CurrencyConversion == "" {
			return &RuleError{"method/currency_conversion", "must name face or exchange"}
		}
		return nil
	case "fixed":
		return m.checkFixed(currency)
	}
	return &RuleError{"method/kind", "is not a way to set a price"}
}

// costPlus has exactly one of the two ratios, and the fixed markup if it has one is a set of prices.
func (m Method) checkCostPlus() error {
	_, hasMarkup, err := ratio("method/markup_ratio", m.MarkupRatio)
	if err != nil {
		return err
	}
	margin, hasMargin, err := ratio("method/margin_ratio", m.MarginRatio)
	switch {
	case err != nil:
		return err
	case hasMarkup == hasMargin:
		return &RuleError{"method", "has exactly one of markup_ratio and margin_ratio"}
	case m.Multiplier != nil || len(m.Schedule) > 0 || m.CurrencyConversion != "":
		return &RuleError{"method", "has members of another kind"}
	case hasMargin && margin.Cmp(big.NewRat(1, 1)) >= 0:
		return &RuleError{"method/margin_ratio", "is below 1"}
	case len(m.FixedMarkup) > 0:
		return checkRates(m.FixedMarkup)
	}
	return nil
}

// A reference multiplier has a multiplier above 0 and a way of reading the reference price's currency. It has no upper
// bound: whether a multiplier takes a price past what a unit price holds depends on the reference price of each model
// the rule applies to, which Check does not see (the catalog's prices are). Such a model then gets no sale price and
// the reason is logged, rather than the rule being refused here or the whole site's prices failing (sale.go).
func (m Method) checkMultiplier() error {
	multiplier, ok, err := ratio("method/multiplier", m.Multiplier)
	switch {
	case err != nil:
		return err
	case !ok || multiplier.Sign() <= 0:
		return &RuleError{"method/multiplier", "is greater than 0"}
	case m.MarkupRatio != nil || m.MarginRatio != nil || len(m.FixedMarkup) > 0 || len(m.Schedule) > 0:
		return &RuleError{"method", "has members of another kind"}
	case m.CurrencyConversion != "" && m.CurrencyConversion != "exchange" && m.CurrencyConversion != "face":
		return &RuleError{"method/currency_conversion", "is exchange or face"}
	}
	return nil
}

// A fixed price is a whole price list in the site's currency and nothing else.
func (m Method) checkFixed(currency platform.Currency) error {
	if len(m.Schedule) == 0 || m.MarkupRatio != nil || m.MarginRatio != nil || len(m.FixedMarkup) > 0 || m.Multiplier != nil || m.CurrencyConversion != "" {
		return &RuleError{"method/schedule", "is the whole of a fixed price"}
	}
	return checkSchedule(m.Schedule, currency)
}

// checkSchedule refuses a fixed price that is no price list of the catalog's form, or is in another currency than the
// site's.
func checkSchedule(raw jsonx.Raw, currency platform.Currency) error {
	s, err := catalog.ParseSchedule(raw)
	if err != nil {
		return &RuleError{"method/schedule", "is not a price list of the catalog's form"}
	}
	if s.Currency() != currency {
		return &RuleError{"method/schedule/currency", "is not the site's currency"}
	}
	return nil
}

// checkRates refuses rates that are no prices per billing item: the same form as a price list's.
func checkRates(rates jsonx.Raw) error {
	if _, err := catalog.ParseSchedule(fmt.Appendf(nil, `{"v":1,"currency":"XXX","rates":%s}`, rates)); err != nil {
		return &RuleError{"method/fixed_markup", "is not a set of prices"}
	}
	return nil
}

// check refuses every member of a constraint: the sale price does not apply constraints in this release
// (docs/domains/billing.md「定价」, #234), so accepting one would store a limit no price obeys. Every way of saving or
// previewing a rule runs Check; a rule stored with one refuses to price instead (Sell).
func (c Constraints) check() error {
	for _, m := range []struct {
		field string
		set   *string
	}{
		{"constraints/min_margin_ratio", c.MinMarginRatio},
		{"constraints/reference_min_ratio", c.ReferenceMinRatio},
		{"constraints/reference_max_ratio", c.ReferenceMaxRatio},
	} {
		if m.set != nil {
			return &RuleError{m.field, "is not supported yet: the sale price does not apply it"}
		}
	}
	return nil
}
