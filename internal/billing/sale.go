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

// ErrRuleRefuses is a rule that matches a model but may not price it: its constraints are stored but not applied by this
// release, so any sale price would silently drop them. Unlike ErrUnpriced, it ends the model's pricing instead of leaving
// the model to the next rule.
var ErrRuleRefuses = errors.New("billing: the rule's constraints are not enforced in this release, so it prices nothing")

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
// docs/domains/billing.md「定价」. The known per-use items are not part of it: every currency has a default price for each
// of them, which admission fills in (docs/domains/billing.md「价目表缺项」), so a fixed price list that leaves one out still
// prices it. What is left is what no default covers, and a call the schedule prices nowhere is charged at the fallback
// prices rather than nothing.
func Unpriced(s catalog.Schedule, r catalog.Reportable) catalog.Reportable {
	r.Items &^= perUseItems
	return s.Missing(r)
}

// Sell is the whole sale price one rule sets for one reference price: a pure function of the rule and the reference
// price, so the same rule versions and reference price always give the same price list, with no database in between.
//
// reference_multiplier with currency_conversion face is the reference price's numbers times the rule's multiplier, read
// as the site's currency (the 中转站 convention: LiteLLM and OpenRouter publish in US dollars, and a site reads those
// numbers as its own). It applies only where the reference price is already in the site's currency: an exchange rate is
// not what face means, and M1 has none. fixed is the rule's own price list. The other ways give no sale price in M1:
// ErrUnpriced for cost_plus, which needs a supplier's cost (M2), for exchange, which needs exchange rates, and for a
// rule that reads a reference price the model has none of (docs/domains/billing.md「定价」, #167, #185). A model without
// a sale price has none: nothing is ever charged at its reference price by default.
//
// A rule that carries constraints is refused with ErrRuleRefuses: this release does not apply them (#234), so pricing it
// would ignore a limit it names. That refusal ends the model's pricing rather than leaving it to a broader rule
// (firstPrice).
func Sell(r Rule, ref catalog.ReferencePrice, site platform.Currency) (catalog.Schedule, error) {
	if r.Constraints != (Constraints{}) {
		return catalog.Schedule{}, ErrRuleRefuses
	}
	switch r.Method.Kind {
	case "reference_multiplier":
		if r.Method.CurrencyConversion != "face" || r.Method.Multiplier == nil || ref.ID == uuid.Nil() || ref.Currency != site.String() {
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

// SalePrices is the sale price of each of models under rules, the rules for one audience in the order they are tried
// (Rules). The first rule that prices a model prices it: a rule for the model beats one for its author, which beats one
// for everything, and a rule that is off, or sets no price in M1's ways, leaves the model to the next. A model no rule
// prices is left out: it has no sale price, so routing leaves its entries out (NoPrice) instead of selling them at the
// reference price.
//
// A rule that does not leave a model to the next is one whose price cannot be made at all: a rule with constraints this
// release does not enforce, which refuses to price the model it matched (RuleRefusal), and a reference price whose
// numbers the catalog cannot hold once multiplied, past a unit price's ceiling. That model is left out, as if no rule
// priced it, and the reason comes back here for the caller to report: one model's price never takes the site's other
// prices with it. Check cannot refuse the overflow before it happens, since the ceiling it passes depends on each model's
// reference price.
//
// A target that is not a model, an author or everything does not price a model in M1: endpoints, groups, suppliers and
// source types are chosen per endpoint, which routing's snapshot does not have yet.
func SalePrices(rules []Rule, snap *catalog.Snapshot, models []catalog.ModelID, currency platform.Currency) (map[catalog.ModelID]Sold, []error) {
	sold := make(map[catalog.ModelID]Sold, len(models))
	var unpriced []error
	// A rule whose price is the same for every model is worked out for the first model it prices and sold alike to the
	// rest: a fixed price list is parsed once however many models the rule prices. Only a price is kept: a refusal is an
	// error and never enters the map, and a rule's constraints do not change while one call runs, so the cache cannot let
	// one model's outcome decide another's.
	same := map[uuid.UUID]Sold{}
	for _, id := range models {
		m, ok := snap.Model(id)
		if !ok {
			continue // not a listed model, which the caller tells the client about
		}
		ref, _ := snap.ReferencePrice(id, currency.String())
		s, priced, err := firstPrice(rules, m, ref, currency, same)
		if err != nil {
			unpriced = append(unpriced, err)
		}
		if priced {
			sold[id] = s
		}
	}
	return sold, unpriced
}

// firstPrice is the price the first rule that prices m gives it, with same holding what rules of one price for every
// model gave already. A rule whose price cannot be made is an error and the model has no price; the later rules are not
// tried, as SalePrices says. A rule with constraints refuses to price, which is that same end: the model is left without
// a sale price rather than priced without the constraint or handed to a broader rule.
func firstPrice(rules []Rule, m catalog.Model, ref catalog.ReferencePrice, currency platform.Currency, same map[uuid.UUID]Sold) (Sold, bool, error) {
	for _, r := range rules {
		if !r.Enabled || !pricesModel(r.Target, m) {
			continue
		}
		if s, done := same[r.ID]; done {
			return s, true, nil
		}
		s, err := Sell(r, ref, currency)
		if errors.Is(err, ErrUnpriced) {
			continue // a rule that sets no price in M1's ways leaves the model to the next
		}
		if errors.Is(err, ErrRuleRefuses) {
			return Sold{}, false, &RuleRefusal{Rule: r.ID, Model: m.ID}
		}
		if err != nil {
			return Sold{}, false, err
		}
		g := Sold{Version: priceVersion(r, ref), Schedule: s}
		if r.Method.SameForEveryModel() {
			same[r.ID] = g
		}
		return g, true, nil
	}
	return Sold{}, false, nil
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
// a version 8 UUID (RFC 9562), a value of our own rather than a random one. A fixed price does not read the reference
// price, so publishing one does not give the fixed rule a new version.
func priceVersion(r Rule, ref catalog.ReferencePrice) uuid.UUID {
	id := ref.ID
	if r.Method.SameForEveryModel() {
		id = uuid.Nil()
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s:%d:%s", r.ID, r.Rev, id))
	var version uuid.UUID
	copy(version[:], sum[:16])
	version[6] = version[6]&0x0f | 0x80
	version[8] = version[8]&0x3f | 0x80
	return version
}
