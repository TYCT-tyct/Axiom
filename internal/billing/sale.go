package billing

import (
	"errors"
	"fmt"
	"math/big"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
)

// Rule-only sale-price arithmetic. Publication adds cost evidence, the site floor and durable versions
// (publication.go); a request locks that published result at admission.

// Sold is one model's sale price and the version it is.
type Sold struct {
	// Audiences contains sparse published layers, compiled by PublishedPrices and WithCosts.
	Audiences *AudiencePrices
	// Version identifies the immutable published row.
	Version uuid.UUID
	// Schedule is the price itself, in the site's currency.
	Schedule      catalog.Schedule
	floor         string
	overrideCosts map[uuid.UUID][]byte
}

// ErrRuleRefuses is a rule that matches a model but may not price it: its constraints are stored but not applied by this
// release, so any sale price would silently drop them. Unlike ErrUnpriced, it ends the model's pricing instead of leaving
// the model to the next rule.
var ErrRuleRefuses = errors.New("billing: the rule's constraints are not enforced in this release, so it prices nothing")

// ErrFreePrice is a sale price the rule produced for a :free model that is not zero. The suffix promises no charge, so
// the model is left unpriced instead of being sold at a paid price.
var ErrFreePrice = errors.New("billing: a :free model's sale price must be zero")

// RuleRefusal is a rule that matched a model and ended its pricing: the model has no sale price, and the later rules are
// not tried. It names the rule and the model, so the reason is auditable wherever the pricing result is reported.
type RuleRefusal struct {
	Rule  uuid.UUID
	Model catalog.ModelID
}

func (e *RuleRefusal) Error() string {
	return fmt.Sprintf("billing: the rule %s refuses to price the model %s: its constraints are not enforced in this release", e.Rule, e.Model)
}

// Unwrap makes a refusal tell itself from a price the catalog cannot hold with errors.Is.
func (*RuleRefusal) Unwrap() error { return ErrRuleRefuses }

// Unpriced is the part of r the sale price s does not price, which keeps the model off routing: the listing threshold of
// docs/domains/billing.md「定价」. The per-use items with a site-wide default (perUseItems) are not part of it: every
// currency has a default price for each of them, which admission fills in (docs/domains/billing.md「价目表缺项」), so a
// fixed price list that leaves one out still prices it. A file search has no default: its schedule must price it, and
// leaving it out keeps the model unlisted. What is left is what no default covers, and a call the schedule prices
// nowhere is charged at the fallback prices rather than nothing.
func Unpriced(s catalog.Schedule, r catalog.Reportable) catalog.Reportable {
	r.Items &^= perUseItems
	return s.Missing(r)
}

// Sell is the whole sale price one rule sets for one reference price: a pure function of the rule and the reference
// price, so the same rule versions and reference price always give the same price list, with no database in between.
//
// reference_multiplier multiplies the selected reference by its multiplier and, for exchange, its exact rate.
// All factors are combined before the schedule rounds once. Same-currency references need no rate; face only accepts
// the site's currency. Publication selects references and persists their exchange evidence. fixed uses the rule's
// own schedule; cost_plus needs publication's endpoint costs. A model without a sale price has none by default.
//
// A rule that carries constraints is refused with ErrRuleRefuses: this release does not apply them (#234), so pricing it
// would ignore a limit it names. That refusal ends the model's pricing rather than leaving it to a broader rule
// (priceFromRules).
func Sell(r Rule, ref catalog.ReferencePrice, site platform.Currency, rates ...platform.ExchangeRate) (catalog.Schedule, error) {
	if r.Constraints != (Constraints{}) {
		return catalog.Schedule{}, ErrRuleRefuses
	}
	switch r.Method.Kind {
	case "reference_multiplier":
		return referenceSale(r, ref, site, rates)
	case "fixed":
		s, err := catalog.ParseSchedule(r.Method.Schedule)
		if err != nil {
			return catalog.Schedule{}, fmt.Errorf("billing: the fixed price of the rule %s: %w", r.ID, err)
		}
		return s, nil
	}
	return catalog.Schedule{}, ErrUnpriced
}

func referenceSale(r Rule, ref catalog.ReferencePrice, site platform.Currency, rates []platform.ExchangeRate) (catalog.Schedule, error) {
	if r.Method.Multiplier == nil || ref.ID == uuid.Nil() {
		return catalog.Schedule{}, ErrUnpriced
	}
	if r.Method.CurrencyConversion != "face" && r.Method.CurrencyConversion != "exchange" || r.Method.CurrencyConversion == "face" && ref.Currency != site.String() {
		return catalog.Schedule{}, ErrUnpriced
	}
	multiplier := *r.Method.Multiplier
	if ref.Schedule.Currency() != site {
		factor, err := saleFactor(r.Method, ref.Schedule.Currency(), site, rates)
		if err != nil {
			return catalog.Schedule{}, err
		}
		multiplier = factor.RatString()
	}
	s, err := ref.Schedule.Scaled(multiplier, site)
	if err != nil {
		return catalog.Schedule{}, fmt.Errorf("billing: price %s by the rule %s: %w", ref.Model, r.ID, err)
	}
	return s, nil
}

func saleFactor(m Method, from, to platform.Currency, rates []platform.ExchangeRate) (*big.Rat, error) {
	factor, ok := new(big.Rat).SetString(*m.Multiplier)
	if !ok || factor.Sign() < 0 {
		return nil, ErrUnpriced
	}
	for _, rate := range rates {
		if value, ok := rate.Between(from, to); ok {
			return factor.Mul(factor, value), nil
		}
	}
	return nil, errors.New("billing: the selected reference price has no exchange rate")
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
