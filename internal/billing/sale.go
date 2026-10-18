package billing

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
)

// A model's sale price: what its calls are charged at, from the moment admission locks it
// (docs/domains/billing.md「定价」). Every model's price comes from its site's pricing rules applied to the model's
// reference price, so the whole of it lives here rather than in a second table of prices.

// Sold is one model's sale price and the version it is.
type Sold struct {
	// Version changes whenever the price does: it is the rule's version and the reference price's row together.
	Version uuid.UUID
	// Schedule is the price itself, in the site's currency.
	Schedule catalog.Schedule
}

// Sell is the whole sale price one rule sets for one reference price: a pure function of the rule and the reference
// price, so the same rule versions and reference price always give the same price list, with no database in between.
//
// reference_multiplier with currency_conversion face is the reference price's numbers times the rule's multiplier, read
// as the site's currency (the 中转站 convention: LiteLLM and OpenRouter publish in US dollars, and a site reads those
// numbers as its own). fixed is the rule's own price list. The other ways give no sale price in M1: ErrUnpriced for
// cost_plus, which needs a supplier's cost (M2), and for exchange, which needs exchange rates
// (docs/domains/billing.md「定价」, #167, #185). A model without a sale price has none: nothing is ever charged at its
// reference price by default.
func Sell(r Rule, ref catalog.ReferencePrice, site platform.Currency) (catalog.Schedule, error) {
	switch r.Method.Kind {
	case "reference_multiplier":
		if r.Method.CurrencyConversion != "face" || r.Method.Multiplier == nil {
			return catalog.Schedule{}, ErrUnpriced
		}
		s, err := ref.Schedule.Scaled(*r.Method.Multiplier, site)
		if err != nil {
			return catalog.Schedule{}, fmt.Errorf("billing: price %s by the rule %s: %w", ref.Model, r.ID, err)
		}
		return s, nil
	case "fixed":
		s, err := catalog.ParseSchedule(r.Method.Schedule)
		if err != nil {
			return catalog.Schedule{}, fmt.Errorf("billing: the fixed price of the rule %s: %w", r.ID, err)
		}
		return s, nil
	}
	return catalog.Schedule{}, ErrUnpriced
}

// SalePrices is each listed model's sale price under rules, the rules for one audience in the order they are tried
// (Rules). The first rule that prices a model prices it: a rule for the model beats one for its author, which beats one
// for everything, and a rule that is off, or sets no price in M1's ways, leaves the model to the next. A model no rule
// prices is left out: it has no sale price, so routing leaves its entries out (NoPrice) instead of selling them at the
// reference price.
//
// A target that is not a model, an author or everything does not price a model in M1: endpoints, groups, suppliers and
// source types are chosen per endpoint, which routing's snapshot does not have yet.
func SalePrices(rules []Rule, snap *catalog.Snapshot, currency platform.Currency) (map[catalog.ModelID]Sold, error) {
	sold := map[catalog.ModelID]Sold{}
	for m := range snap.Models() {
		ref, hasRef := snap.ReferencePrice(m.ID, currency.String())
		for _, r := range rules {
			if !r.Enabled || !pricesModel(r.Target, m) || !hasRef && r.Method.Kind == "reference_multiplier" {
				continue
			}
			s, err := Sell(r, ref, currency)
			if errors.Is(err, ErrUnpriced) {
				continue
			}
			if err != nil {
				return nil, err
			}
			sold[m.ID] = Sold{Version: priceVersion(r, ref), Schedule: s}
			break
		}
	}
	return sold, nil
}

// pricesModel: the rule's target holds the model.
func pricesModel(t Selector, m catalog.Model) bool {
	switch t.Kind {
	case "all":
		return true
	case "model":
		return t.ID == m.ID.String()
	case "author":
		return t.ID == m.Author
	}
	return false
}

// priceVersion identifies a sale price: the rule's ID and rev and the reference price's row. A digest makes the version
// change with any of them, so the same inputs always give the same version and a changed price never reuses one. It is
// a version 8 UUID (RFC 9562), a value of our own rather than a random one.
func priceVersion(r Rule, ref catalog.ReferencePrice) uuid.UUID {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s:%d:%s", r.ID, r.Rev, ref.ID))
	var version uuid.UUID
	copy(version[:], sum[:16])
	version[6] = version[6]&0x0f | 0x80
	version[8] = version[8]&0x3f | 0x80
	return version
}
