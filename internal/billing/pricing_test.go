package billing

import (
	"errors"
	"slices"
	"testing"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

var usd = func() platform.Currency {
	c, err := platform.ParseCurrency("USD")
	if err != nil {
		panic(err) // a constant
	}
	return c
}()

const (
	model   = "0198c0a8-0000-7000-8000-000000000001"
	tierID  = "tier_00000000000000000000000000"
	acctID  = "acct_00000000000000000000000000"
	fixedUS = `{"v":1,"currency":"USD","rates":{"input":"0.000002","output":"0.000004"}}`
)

func plus(markup string) Method { return Method{Kind: "cost_plus", MarkupRatio: &markup} }

// rule is a valid rule of the method for the target and everyone.
func rule(target Selector, m Method) RuleInput {
	return RuleInput{Enabled: true, Target: target, Audience: Selector{Kind: "all"}, Method: m}
}

// TestRuleChecks: a rule breaks a rule of rules by one member, which the refusal names; the rules that pass are the
// ones the contract describes.
func TestRuleChecks(t *testing.T) {
	t.Parallel()
	ratio := func(s string) *string { return &s }
	all, multiplier := Selector{Kind: "all"}, func(s string) Method { return Method{Kind: "reference_multiplier", Multiplier: ratio(s)} }
	for name, c := range map[string]struct {
		in    RuleInput
		field string
	}{
		"cost plus a markup":                          {rule(all, plus("0.25")), ""},
		"cost plus a margin and fixed":                {rule(all, Method{Kind: "cost_plus", MarginRatio: ratio("0.2"), FixedMarkup: jsonx.Raw(`{"output":"0.000001"}`)}), ""},
		"a multiplier by exchange":                    {rule(all, multiplier("1.5")), ""},
		"a multiplier at face":                        {rule(all, Method{Kind: "reference_multiplier", Multiplier: ratio("2"), CurrencyConversion: "face"}), ""},
		"a fixed price":                               {rule(all, Method{Kind: "fixed", Schedule: jsonx.Raw(fixedUS)}), ""},
		"an endpoint of a UUID":                       {rule(Selector{Kind: "endpoint", ID: model}, plus("0")), ""},
		"an author":                                   {rule(Selector{Kind: "author", ID: "acme"}, plus("0")), ""},
		"a tier, which waits for a later release":     {RuleInput{Target: all, Audience: Selector{Kind: "tier", ID: tierID}, Method: plus("1")}, "audience/kind"},
		"an account, which waits for a later release": {RuleInput{Target: all, Audience: Selector{Kind: "account", ID: acctID}, Method: plus("1")}, "audience/kind"},
		"a kind that prices nothing":                  {rule(Selector{Kind: "planet"}, plus("0")), "target/kind"},
		"all with an ID":                              {rule(Selector{Kind: "all", ID: "x"}, plus("0")), "target/id"},
		"a model without an ID":                       {rule(Selector{Kind: "model"}, plus("0")), "target/id"},
		"a model of an upper case ID":                 {rule(Selector{Kind: "model", ID: "0198C0A8-0000-7000-8000-000000000001"}, plus("0")), "target/id"},
		"an author that is no slug":                   {rule(Selector{Kind: "author", ID: "Acme"}, plus("0")), "target/id"},
		"an audience that is none":                    {RuleInput{Target: all, Audience: Selector{Kind: "crowd"}, Method: plus("0")}, "audience/kind"},
		"everyone with an ID":                         {RuleInput{Target: all, Audience: Selector{Kind: "all", ID: tierID}, Method: plus("0")}, "audience/id"},
		"a method that is none":                       {rule(all, Method{Kind: "free"}), "method/kind"},
		"a markup and a margin":                       {rule(all, Method{Kind: "cost_plus", MarkupRatio: ratio("1"), MarginRatio: ratio("0.5")}), "method"},
		"neither markup nor margin":                   {rule(all, Method{Kind: "cost_plus"}), "method"},
		"a negative markup":                           {rule(all, plus("-0.1")), "method/markup_ratio"},
		"a markup that is no decimal":                 {rule(all, plus("1/2")), "method/markup_ratio"},
		"a margin of one":                             {rule(all, Method{Kind: "cost_plus", MarginRatio: ratio("1")}), "method/margin_ratio"},
		"cost plus with a multiplier":                 {rule(all, Method{Kind: "cost_plus", MarkupRatio: ratio("1"), Multiplier: ratio("2")}), "method"},
		"fixed markup that is no prices":              {rule(all, Method{Kind: "cost_plus", MarkupRatio: ratio("1"), FixedMarkup: jsonx.Raw(`{"input":"-1"}`)}), "method/fixed_markup"},
		"a multiplier of nothing":                     {rule(all, multiplier("0")), "method/multiplier"},
		"a multiplier left out":                       {rule(all, Method{Kind: "reference_multiplier"}), "method/multiplier"},
		"a multiplier with a markup":                  {rule(all, Method{Kind: "reference_multiplier", Multiplier: ratio("2"), MarkupRatio: ratio("1")}), "method"},
		"a conversion that is none":                   {rule(all, Method{Kind: "reference_multiplier", Multiplier: ratio("2"), CurrencyConversion: "magic"}), "method/currency_conversion"},
		"a fixed price without prices":                {rule(all, Method{Kind: "fixed"}), "method/schedule"},
		"a fixed price with a markup":                 {rule(all, Method{Kind: "fixed", Schedule: jsonx.Raw(fixedUS), MarkupRatio: ratio("1")}), "method/schedule"},
		"a fixed price that is no prices":             {rule(all, Method{Kind: "fixed", Schedule: jsonx.Raw(`{"v":1}`)}), "method/schedule"},
		"a fixed price in another money":              {rule(all, Method{Kind: "fixed", Schedule: jsonx.Raw(`{"v":1,"currency":"CNY","rates":{"input":"1"}}`)}), "method/schedule/currency"},
	} {
		err := c.in.Check(usd)
		if refusal, ok := errors.AsType[*RuleError](err); c.field == "" && err != nil || c.field != "" && (!ok || refusal.Field != c.field) {
			t.Errorf("%s: %v, want a refusal of %q", name, err, c.field)
		}
	}
}

// TestConstraintChecks: the limits a rule puts on its price are ratios that make sense together.
func TestConstraintChecks(t *testing.T) {
	t.Parallel()
	r := func(s string) *string { return &s }
	for name, c := range map[string]struct {
		c     Constraints
		field string
	}{
		"none":                          {Constraints{}, ""},
		"a margin and a band":           {Constraints{MinMarginRatio: r("0.1"), ReferenceMinRatio: r("0.5"), ReferenceMaxRatio: r("3")}, ""},
		"a band of one price":           {Constraints{ReferenceMinRatio: r("1"), ReferenceMaxRatio: r("1")}, ""},
		"a margin of one":               {Constraints{MinMarginRatio: r("1")}, "constraints/min_margin_ratio"},
		"a margin that is not a number": {Constraints{MinMarginRatio: r("x")}, "constraints/min_margin_ratio"},
		"a floor of nothing":            {Constraints{ReferenceMinRatio: r("0")}, "constraints/reference_min_ratio"},
		"a ceiling of nothing":          {Constraints{ReferenceMaxRatio: r("0")}, "constraints/reference_min_ratio"},
		"a band turned around":          {Constraints{ReferenceMinRatio: r("2"), ReferenceMaxRatio: r("1")}, "constraints/reference_min_ratio"},
	} {
		err := RuleInput{Target: Selector{Kind: "all"}, Audience: Selector{Kind: "all"}, Method: plus("0"), Constraints: c.c}.Check(usd)
		if refusal, ok := errors.AsType[*RuleError](err); c.field == "" && err != nil || c.field != "" && (!ok || refusal.Field != c.field) {
			t.Errorf("%s: %v, want a refusal of %q", name, err, c.field)
		}
	}
}

// rules is a billing database with the platform scope.
type rules struct {
	t    *testing.T
	db   *pg.DB
	site uuid.UUID
}

func newRules(t *testing.T) rules {
	t.Helper()
	db, err := pg.Open(t.Context(), pgtest.New(t, Migrations()).App)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return rules{t, db, uuid.NewV7()}
}

var system = audit.System("billing.test")

func (r rules) tx(f func(tx pg.Tx) error) error {
	return r.db.Tx(r.t.Context(), pg.NewScope(true, nil, nil), f)
}

func (r rules) create(in RuleInput) (Rule, error) {
	var made Rule
	err := r.tx(func(tx pg.Tx) (err error) {
		made, err = CreateRule(r.t.Context(), tx, system, r.site, usd, in, "because")
		return err
	})
	return made, err
}

// store writes a rule straight into the table, as a release that lets an administrator make a rule for one account or
// one tier will have it: CreateRule refuses those now.
func (r rules) store(in RuleInput) {
	r.t.Helper()
	method, err := jsonx.MarshalCanonical(in.Method)
	if err != nil {
		r.t.Fatal(err)
	}
	if err := r.tx(func(tx pg.Tx) error {
		_, err := tx.Exec(r.t.Context(), `INSERT INTO billing.pricing_rules
			(id, site_id, enabled, target_kind, target_id, audience_kind, audience_id, method, constraints)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, '{}'::jsonb)`,
			uuid.NewV7(), r.site, in.Enabled, in.Target.Kind, in.Target.ID, in.Audience.Kind, in.Audience.ID, method)
		return err
	}); err != nil {
		r.t.Fatal(err)
	}
}

func (r rules) update(id uuid.UUID, rev int64, in RuleInput) (Rule, error) {
	var made Rule
	err := r.tx(func(tx pg.Tx) (err error) {
		made, err = UpdateRule(r.t.Context(), tx, system, r.site, usd, id, rev, in, "changed")
		return err
	})
	return made, err
}

func (r rules) list() []Rule {
	r.t.Helper()
	var got []Rule
	if err := r.tx(func(tx pg.Tx) (err error) {
		got, err = Rules(r.t.Context(), tx, r.site)
		return err
	}); err != nil {
		r.t.Fatal(err)
	}
	return got
}

func (r rules) count(table string) int {
	r.t.Helper()
	var n int
	if err := r.tx(func(tx pg.Tx) error {
		return tx.QueryRow(r.t.Context(), "SELECT count(*) FROM "+table).Scan(&n)
	}); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// TestRulesAreTriedMostSpecificFirst: the rules of a site come in the order they are tried, the target first and the
// audience within it, whatever the order they were made in.
func TestRulesAreTriedMostSpecificFirst(t *testing.T) {
	t.Parallel()
	r := newRules(t)
	tier := Selector{Kind: "tier", ID: tierID}
	for _, in := range []RuleInput{
		rule(Selector{Kind: "all"}, plus("1")),
		{Enabled: true, Target: Selector{Kind: "model", ID: model}, Audience: Selector{Kind: "all"}, Method: plus("1")},
		{Enabled: true, Target: Selector{Kind: "model", ID: model}, Audience: tier, Method: plus("1")},
		{Enabled: false, Target: Selector{Kind: "endpoint", ID: model}, Audience: Selector{Kind: "account", ID: acctID}, Method: plus("1")},
		{Enabled: true, Target: Selector{Kind: "author", ID: "acme"}, Audience: Selector{Kind: "all"}, Method: plus("1")},
	} {
		if in.Audience.Kind == "all" {
			if _, err := r.create(in); err != nil {
				t.Fatal(err)
			}
			continue
		}
		r.store(in) // CreateRule refuses the audiences of a later release; the order they will be tried in is already fixed
	}
	var order []string
	for _, rule := range r.list() {
		order = append(order, rule.Target.Kind+"/"+rule.Audience.Kind)
	}
	want := []string{"endpoint/account", "model/tier", "model/all", "author/all", "all/all"}
	if !slices.Equal(order, want) {
		t.Errorf("the order: %v, want %v", order, want)
	}
}

// TestRuleChanges: a rule is changed as of the rev it was read at, which then goes up; a change as of another rev, or of
// a rule that is not there, is refused.
func TestRuleChanges(t *testing.T) {
	t.Parallel()
	r := newRules(t)
	first, err := r.create(rule(Selector{Kind: "all"}, plus("1")))
	if err != nil {
		t.Fatal(err)
	}
	disabled := rule(Selector{Kind: "all"}, plus("2"))
	disabled.Enabled = false
	changed, err := r.update(first.ID, 0, disabled)
	if err != nil || changed.Rev != first.Rev+1 || changed.Enabled {
		t.Fatalf("a change: %+v %v", changed, err)
	}
	if again, err := r.update(first.ID, 1, rule(Selector{Kind: "all"}, plus("3"))); err != nil || again.Rev != 2 || !again.Enabled {
		t.Errorf("a change as of the new rev: %+v %v", again, err)
	}
	if _, err := r.update(first.ID, 0, disabled); !errors.Is(err, ErrStaleRule) {
		t.Errorf("a change as of a rev the rule left: %v", err)
	}
	if _, err := r.update(uuid.NewV7(), 0, disabled); !errors.Is(err, ErrRuleNotFound) {
		t.Errorf("a rule that is not there: %v", err)
	}
}

// TestRulesAreUniqueByTargetAndAudience: a rule of the same target and audience as another cannot be made or changed
// into, and the refusal changes nothing.
func TestRulesAreUniqueByTargetAndAudience(t *testing.T) {
	t.Parallel()
	r := newRules(t)
	if _, err := r.create(rule(Selector{Kind: "all"}, plus("2"))); err != nil {
		t.Fatal(err)
	}
	if _, err := r.create(rule(Selector{Kind: "all"}, plus("3"))); !errors.Is(err, ErrRuleExists) {
		t.Errorf("a second rule of one target and audience: %v", err)
	}
	other, err := r.create(rule(Selector{Kind: "author", ID: "acme"}, plus("1")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.update(other.ID, 0, rule(Selector{Kind: "all"}, plus("1"))); !errors.Is(err, ErrRuleExists) {
		t.Errorf("a change into another rule's target and audience: %v", err)
	}
	got := r.list()
	if len(got) != 2 || *got[0].Method.MarkupRatio != "1" || got[0].Target.Kind != "author" || got[0].Rev != 0 || *got[1].Method.MarkupRatio != "2" {
		t.Errorf("the rules after the refusals: %+v", got)
	}
}

// TestRuleHistoryAndAudit: each creation and each change leaves its version, with who made it and why, and an audit
// entry; a refused change leaves neither.
func TestRuleHistoryAndAudit(t *testing.T) {
	t.Parallel()
	r := newRules(t)
	made, err := r.create(rule(Selector{Kind: "all"}, plus("1")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.update(made.ID, 0, rule(Selector{Kind: "all"}, plus("2"))); err != nil {
		t.Fatal(err)
	}
	_, _ = r.update(made.ID, 0, rule(Selector{Kind: "all"}, plus("9"))) //nolint:errcheck // refused; the count below shows nothing was kept
	if versions, entries := r.count("billing.pricing_rule_versions"), r.count("platform.audit_log"); versions != 2 || entries != 2 {
		t.Errorf("%d versions and %d audit entries, want 2 and 2", versions, entries)
	}
	var latest, actor string
	if err := r.tx(func(tx pg.Tx) error {
		return tx.QueryRow(r.t.Context(), `SELECT body->'method'->>'markup_ratio', actor || '|' || reason FROM billing.pricing_rule_versions
			WHERE rule_id = $1 AND rev = 1`, made.ID).Scan(&latest, &actor)
	}); err != nil || latest != "2" || actor != "system:billing.test|changed" {
		t.Errorf("the second version: %q %q %v", latest, actor, err)
	}
}
