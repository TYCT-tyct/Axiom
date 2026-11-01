package billing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// The pricing rules of a site (docs/domains/billing.md「定价」, README.md「定价规则」). Each function takes the caller's
// transaction, which must hold the platform scope.

// The ways a change of a rule is refused.
var (
	// ErrRuleNotFound is a rule the site does not have.
	ErrRuleNotFound = errors.New("billing: no such pricing rule")
	// ErrRuleExists is a rule with the target and audience another rule of the site has.
	ErrRuleExists = errors.New("billing: the site has a pricing rule for that target and audience")
	// ErrStaleRule is a change decided on a rev the rule has left.
	ErrStaleRule = errors.New("billing: the pricing rule changed since it was read")
)

// RuleError is a rule that breaks what the contract's schema cannot say: the member it is about, and which rule it
// breaks (never the value).
type RuleError struct{ Field, Reason string }

func (e *RuleError) Error() string { return "billing: pricing rule " + e.Field + " " + e.Reason }

// Selector is what a rule prices or whom it prices for: a kind, and the ID of the thing unless the kind is "all".
type Selector struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

// Prices reports whether the rule prices for an account of a tier: a rule for an account prices that account alone, one
// for a tier accounts of that tier, and one for all, everyone. A zero account or tier stands for an audience that has
// none, so only a rule for all prices it. It is what a pricing preview asks to keep the rules of the audience it shows.
func (s Selector) Prices(account accounts.AccountID, tier accounts.TierID) bool {
	switch s.Kind {
	case "account":
		return account != accounts.AccountID{} && s.ID == account.TypeID()
	case "tier":
		return tier != accounts.TierID{} && s.ID == tier.TypeID()
	}
	return true
}

// Method is how a rule sets the price. Which members a kind uses is checked by Check; the others are absent.
type Method struct {
	Kind               string    `json:"kind"`
	MarkupRatio        *string   `json:"markup_ratio,omitempty"`
	MarginRatio        *string   `json:"margin_ratio,omitempty"`
	FixedMarkup        jsonx.Raw `json:"fixed_markup,omitempty"`
	Multiplier         *string   `json:"multiplier,omitempty"`
	CurrencyConversion string    `json:"currency_conversion,omitempty"`
	Schedule           jsonx.Raw `json:"schedule,omitempty"`
}

// Constraints are limits on the price a rule sets, as ratios. This release does not apply them (#234): Check refuses a
// rule that carries one, and a stored one refuses to price the models it matches (Sell).
type Constraints struct {
	MinMarginRatio    *string `json:"min_margin_ratio,omitempty"`
	ReferenceMinRatio *string `json:"reference_min_ratio,omitempty"`
	ReferenceMaxRatio *string `json:"reference_max_ratio,omitempty"`
}

// RuleInput is what a person decides about a rule.
type RuleInput struct {
	Enabled     bool
	Target      Selector
	Audience    Selector
	Method      Method
	Constraints Constraints
}

// Rule is a rule as it is stored.
type Rule struct {
	RuleInput
	ID        uuid.UUID
	Rev       int64
	UpdatedAt time.Time
}

var (
	decimal    = regexp.MustCompile(`^(0|[1-9][0-9]{0,17})(\.[0-9]{1,15})?$`)
	authorSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	sourceType = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// targets and audiences by how specific they are: the first is tried first.
var (
	targetKinds   = []string{"endpoint", "group", "supplier", "model", "author", "source_type", "all"}
	audienceKinds = []string{"account", "tier", "all"}
)

// honouredTargets are the targets a rule prices in M1 (sale.go pricesModel): the others are chosen per endpoint, which
// the routing snapshot does not have yet.
var honouredTargets = []string{"model", "author", "all"}

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

// specificity is how early a rule is tried: its target, from the most specific, then its audience.
func (r Rule) specificity() [2]int {
	return [2]int{slices.Index(targetKinds, r.Target.Kind), slices.Index(audienceKinds, r.Audience.Kind)}
}

func ruleOf(id uuid.UUID, rev int64, updated time.Time, enabled bool, tk, ti, ak, ai string, method, constraints []byte) (Rule, error) {
	r := Rule{ID: id, Rev: rev, UpdatedAt: updated}
	r.Enabled, r.Target, r.Audience = enabled, Selector{tk, ti}, Selector{ak, ai}
	if err := jsonx.Unmarshal(method, &r.Method); err != nil {
		return Rule{}, fmt.Errorf("billing: pricing rule %s: its method: %w", id, err)
	}
	if err := jsonx.Unmarshal(constraints, &r.Constraints); err != nil {
		return Rule{}, fmt.Errorf("billing: pricing rule %s: its constraints: %w", id, err)
	}
	return r, nil
}

// Rules are the site's rules in the order they are tried: the most specific target first, then the most specific
// audience, then by ID.
func Rules(ctx context.Context, tx pg.Tx, site uuid.UUID) ([]Rule, error) {
	rows, err := queries.ListPricingRules(ctx, tx, site)
	if err != nil {
		return nil, fmt.Errorf("billing: list the pricing rules: %w", err)
	}
	rules := make([]Rule, len(rows))
	for i, r := range rows {
		if rules[i], err = ruleOf(r.ID, r.Rev, r.UpdatedAt, r.Enabled, r.TargetKind, r.TargetID, r.AudienceKind, r.AudienceID, r.Method, r.Constraints); err != nil {
			return nil, err
		}
	}
	slices.SortFunc(rules, func(a, b Rule) int {
		sa, sb := a.specificity(), b.specificity()
		if c := slices.Compare(sa[:], sb[:]); c != 0 {
			return c
		}
		return slices.Compare(a.ID[:], b.ID[:]) // a rule of each target and audience at most, so this is a total order
	})
	return rules, nil
}

// Place is rules with r among them, in the order they are tried: a rule not stored yet, which a pricing preview asks
// about. It goes after the rules it ties with, so a rule that is no more specific than those there never displaces one;
// an ID of its own, which only a stored rule has, does not decide where it goes.
func Place(r Rule, rules []Rule) []Rule {
	s := r.specificity()
	i := slices.IndexFunc(rules, func(o Rule) bool {
		at := o.specificity()
		return slices.Compare(at[:], s[:]) > 0
	})
	if i < 0 {
		i = len(rules)
	}
	out := make([]Rule, 0, len(rules)+1)
	out = append(out, rules[:i]...)
	out = append(append(out, r), rules[i:]...)
	return out
}

// CreateRule adds a rule to the site. The change is recorded as the rule's first version and in the audit log.
func CreateRule(ctx context.Context, tx pg.Tx, actor audit.Actor, site uuid.UUID, currency platform.Currency, in RuleInput, allow func(catalog.Schedule) error, reason string) (Rule, error) {
	if err := in.Check(currency); err != nil {
		return Rule{}, err
	}
	if err := in.CheckItems(allow); err != nil {
		return Rule{}, err
	}
	if _, err := LockPricePolicy(ctx, tx, site); err != nil {
		return Rule{}, err
	}
	method, constraints, err := encoded(in)
	if err != nil {
		return Rule{}, err
	}
	r := Rule{RuleInput: in, ID: uuid.NewV7()}
	r.UpdatedAt, err = queries.InsertPricingRule(ctx, tx, InsertPricingRuleParams{
		ID: r.ID, SiteID: site, Enabled: in.Enabled, TargetKind: in.Target.Kind, TargetID: in.Target.ID,
		AudienceKind: in.Audience.Kind, AudienceID: in.Audience.ID, Method: method, Constraints: constraints,
	})
	if _, duplicate := pg.Duplicate(err); duplicate {
		return Rule{}, ErrRuleExists
	}
	if err != nil {
		return Rule{}, fmt.Errorf("billing: add the pricing rule: %w", err)
	}
	return r, record(ctx, tx, actor, "pricing_rule.create", r, reason)
}

// UpdateRule replaces the rule as of rev. validate checks external subjects after locking the stored rule;
// only unchanged retirement bypasses validation, so legacy rules can always be stopped.
func UpdateRule(ctx context.Context, tx pg.Tx, actor audit.Actor, site uuid.UUID, currency platform.Currency, id uuid.UUID, rev int64, in RuleInput, allow func(catalog.Schedule) error, reason string, validate func() error) (Rule, error) {
	if _, err := LockPricePolicy(ctx, tx, site); err != nil {
		return Rule{}, err
	}
	method, constraints, err := encoded(in)
	if err != nil {
		return Rule{}, err
	}
	locked, err := queries.LockPricingRule(ctx, tx, LockPricingRuleParams{ID: id, SiteID: site})
	switch {
	case err != nil:
		return Rule{}, fmt.Errorf("billing: lock the pricing rule: %w", err)
	case len(locked) == 0:
		return Rule{}, ErrRuleNotFound
	case locked[0].Rev != rev:
		return Rule{}, fmt.Errorf("%w: it is at rev %d, the request is for %d", ErrStaleRule, locked[0].Rev, rev)
	}
	old := locked[0]
	before, err := ruleOf(old.ID, old.Rev, old.UpdatedAt, old.Enabled, old.TargetKind, old.TargetID, old.AudienceKind, old.AudienceID, old.Method, old.Constraints)
	if err != nil {
		return Rule{}, err
	}
	if err := checkRuleUpdate(before, in, currency, allow, validate); err != nil {
		return Rule{}, err
	}
	r := Rule{RuleInput: in, ID: id}
	updated, err := queries.UpdatePricingRule(ctx, tx, UpdatePricingRuleParams{
		ID: id, Enabled: in.Enabled, TargetKind: in.Target.Kind, TargetID: in.Target.ID,
		AudienceKind: in.Audience.Kind, AudienceID: in.Audience.ID, Method: method, Constraints: constraints,
	})
	if _, duplicate := pg.Duplicate(err); duplicate {
		return Rule{}, ErrRuleExists
	}
	if err != nil {
		return Rule{}, fmt.Errorf("billing: change the pricing rule: %w", err)
	}
	r.Rev, r.UpdatedAt = updated.Rev, updated.UpdatedAt
	return r, record(ctx, tx, actor, "pricing_rule.update", r, reason)
}

func checkRuleUpdate(before Rule, in RuleInput, currency platform.Currency, allow func(catalog.Schedule) error, validate func() error) error {
	retiring, err := unchangedRetirement(before, in)
	if err != nil || retiring {
		return err
	}
	if err := in.Check(currency); err != nil {
		return err
	}
	if err := in.CheckItems(allow); err != nil {
		return err
	}
	if validate != nil {
		return validate()
	}
	return nil
}

// unchangedRetirement compares the saved input before permitting retirement without subject validation.
func unchangedRetirement(before Rule, in RuleInput) (bool, error) {
	if in.Enabled {
		return false, nil
	}
	retired := before.RuleInput
	retired.Enabled = false
	// JSONB and the HTTP codec can change member order and whitespace without changing the stored rule.
	old, err := jsonx.MarshalCanonical(retired)
	if err != nil {
		return false, err
	}
	next, err := jsonx.MarshalCanonical(in)
	if err != nil {
		return false, err
	}
	return bytes.Equal(old, next), nil
}

func encoded(in RuleInput) (method, constraints []byte, err error) {
	if method, err = jsonx.MarshalCanonical(in.Method); err != nil {
		return nil, nil, fmt.Errorf("billing: encode the method: %w", err)
	}
	if constraints, err = jsonx.MarshalCanonical(in.Constraints); err != nil {
		return nil, nil, fmt.Errorf("billing: encode the constraints: %w", err)
	}
	return method, constraints, nil
}

// record keeps the rule as it is now in the history and in the audit log, in the transaction of the change.
func record(ctx context.Context, tx pg.Tx, actor audit.Actor, action string, r Rule, reason string) error {
	body, err := jsonx.MarshalCanonical(struct {
		Enabled     bool        `json:"enabled"`
		Target      Selector    `json:"target"`
		Audience    Selector    `json:"audience"`
		Method      Method      `json:"method"`
		Constraints Constraints `json:"constraints"`
	}{r.Enabled, r.Target, r.Audience, r.Method, r.Constraints})
	if err != nil {
		return fmt.Errorf("billing: encode the pricing rule: %w", err)
	}
	if err := queries.InsertPricingRuleVersion(ctx, tx, InsertPricingRuleVersionParams{
		RuleID: r.ID, Rev: r.Rev, Body: body, Actor: actor.String(), Reason: reason,
	}); err != nil {
		return fmt.Errorf("billing: keep the pricing rule's version: %w", err)
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: action, Reason: reason,
		Target: audit.Target{Module: "billing", Kind: "pricing_rule", ID: r.ID.String()},
		Detail: map[string]any{"rev": r.Rev, "enabled": r.Enabled, "target": r.Target, "audience": r.Audience, "method": r.Method.Kind},
	})
}
