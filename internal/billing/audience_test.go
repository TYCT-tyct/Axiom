package billing

import (
	"errors"
	"slices"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func (f *priceFixture) published() Sold {
	f.t.Helper()
	var prices map[catalog.ModelID]Sold
	if err := f.tx(func(tx pg.Tx) (err error) { prices, err = PublishedPrices(f.t.Context(), tx, f.site); return err }); err != nil {
		f.t.Fatal(err)
	}
	return prices[f.model]
}

func TestAudiencePricesAndRetirement(t *testing.T) {
	t.Parallel()

	f := newPrices(t)
	account := accounts.AccountID(uuid.UUID{15: 1})
	tier := accounts.TierID(uuid.UUID{15: 1})
	a := Audience{Account: account, Tier: tier}
	makeRule := func(audience Selector, multiplier string) Rule {
		in := rule(Selector{Kind: "all"}, faced(multiplier))
		in.Audience = audience
		r, err := f.create(in)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	tr := makeRule(Selector{Kind: "tier", ID: tierID}, "2")
	ar := makeRule(Selector{Kind: "account", ID: acctID}, "3")
	f.refresh()
	check := func(a Audience, want string) Sold {
		t.Helper()
		p := f.published().For(a)
		rate, ok := p.Schedule.BaseRate(catalog.ItemInput)
		if !ok || rate.String() != want || p.Version == uuid.Nil() {
			t.Fatalf("audience %+v: %s, want %s", a, rate.String(), want)
		}
		return p
	}
	check(Audience{}, "0.000001")
	check(Audience{Tier: tier}, "0.000002")
	first := check(a, "0.000003")
	// A tier change is read at admission, not captured on the account's publication.
	check(Audience{Account: account}, "0.000003")
	specific := rule(Selector{Kind: "model", ID: f.model.String()}, faced("1.05"))
	mr, err := f.create(specific)
	if err != nil {
		t.Fatal(err)
	}
	f.refresh()
	check(a, "0.00000105")
	mr.Enabled = false
	if _, err = f.update(mr.ID, mr.Rev, mr.RuleInput); err != nil {
		t.Fatal(err)
	}
	f.refresh()
	check(a, "0.000003")
	ar.Enabled = false
	if _, err = f.update(ar.ID, ar.Rev, ar.RuleInput); err != nil {
		t.Fatal(err)
	}
	f.refresh()
	check(a, "0.000002")
	tr.Enabled = false
	if _, err = f.update(tr.ID, tr.Rev, tr.RuleInput); err != nil {
		t.Fatal(err)
	}
	f.refresh()
	check(a, "0.000001")
	if first.Version == f.published().For(a).Version {
		t.Fatal("retirement kept account version")
	}
}

func TestAudienceFloorAndConfirmation(t *testing.T) {
	t.Parallel()

	f := newPrices(t)
	account := accounts.AccountID(uuid.UUID{15: 1})
	a := Audience{Account: account}
	in := rule(Selector{Kind: "all"}, faced("0.1"))
	in.Audience = Selector{Kind: "account", ID: acctID}
	r, err := f.create(in)
	if err != nil {
		t.Fatal(err)
	}
	f.refresh()
	// Raising 0.1 to the floor exceeds the default automatic increase cap, so this audience stops.
	if p := f.published().For(a); p.Version != uuid.Nil() {
		t.Fatal("blocked account fell back to default price")
	}
	var state []Publication
	if err = f.tx(func(tx pg.Tx) (err error) { _, state, err = PriceState(t.Context(), tx, f.site); return err }); err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(state, func(p Publication) bool { return p.Audience.Kind == "account" })
	if at < 0 {
		t.Fatal("missing audience proposal")
	}
	if err = f.publish(state[at], true, false); err != nil {
		t.Fatal(err)
	}
	safe := f.published().For(a)
	cost, err := CostEnvelope(f.costs, usd)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := safe.Schedule.CoversCost(cost); err != nil || !ok {
		t.Fatal("confirmed audience below floor", err)
	}
	in.Method = faced("4")
	if _, err = f.update(r.ID, r.Rev, in); err != nil {
		t.Fatal(err)
	}
	f.refresh()
	if got := f.published().For(a); got.Version != safe.Version {
		t.Fatal("large audience move skipped confirmation")
	}
	f.costs[0].Schedule = mustSchedule(t, `{"v":1,"currency":"XXX","rates":{"input":"0.00001","output":"0.00002"}}`)
	f.refresh()
	if got := f.published().For(a); got.Version != uuid.Nil() {
		t.Fatal("audience kept unsafe price after costs changed")
	}
}

func TestLegacyRulesCanAlwaysBeRetired(t *testing.T) {
	t.Parallel()

	for _, change := range []func(*RuleInput){
		func(in *RuleInput) { in.Target = Selector{Kind: "endpoint", ID: model} },
		func(in *RuleInput) { v := "0.2"; in.Constraints.MinMarginRatio = &v },
		func(in *RuleInput) { in.Method.CurrencyConversion = "" },
	} {
		f := newRules(t)
		in := rule(Selector{Kind: "all"}, faced("1"))
		change(&in)
		// Seed the historical input, including fields a current save refuses.
		method, constraints, err := encoded(in)
		if err != nil {
			t.Fatal(err)
		}
		id := uuid.NewV7()
		if err = f.tx(func(tx pg.Tx) error {
			_, err := queries.InsertPricingRule(t.Context(), tx, InsertPricingRuleParams{ID: id, SiteID: f.site, Enabled: true, TargetKind: in.Target.Kind, TargetID: in.Target.ID, AudienceKind: in.Audience.Kind, AudienceID: in.Audience.ID, Method: method, Constraints: constraints})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		in.Enabled = false
		retired, err := f.update(id, 0, in)
		if err != nil || retired.Enabled {
			t.Fatal("legacy retirement failed", err)
		}
		in.Enabled = true
		if _, err = f.update(id, retired.Rev, in); err == nil {
			t.Fatal("unsupported legacy rule re-enabled")
		}
		if _, err = f.update(id, 0, retired.RuleInput); !errors.Is(err, ErrStaleRule) {
			t.Fatal("stale retirement accepted", err)
		}
	}
}

func TestAudienceSelectorEditRetiresOldPublication(t *testing.T) {
	t.Parallel()

	f := newPrices(t)
	f.catalog.model("acme/other-audience")
	oldTier := accounts.TierID(uuid.UUID{15: 1})
	newTier := accounts.TierID(uuid.UUID{15: 2})
	in := rule(Selector{Kind: "model", ID: f.model.String()}, faced("2"))
	in.Audience = Selector{Kind: "tier", ID: oldTier.TypeID()}
	made, err := f.create(in)
	if err != nil {
		t.Fatal(err)
	}
	f.refresh()
	in.Audience.ID = newTier.TypeID()
	if _, err = f.update(made.ID, made.Rev, in); err != nil {
		t.Fatal(err)
	}
	f.refresh()
	for _, c := range []struct {
		tier accounts.TierID
		want string
	}{{oldTier, "0.000001"}, {newTier, "0.000002"}} {
		price := f.published().For(Audience{Tier: c.tier})
		rate, ok := price.Schedule.BaseRate(catalog.ItemInput)
		if !ok || rate.String() != c.want {
			t.Fatalf("tier %s: got %s, want %s", c.tier.TypeID(), rate.String(), c.want)
		}
	}
	if got := f.count("billing.audience_publications"); got != 2 {
		t.Fatalf("%d scoped rows, want only the old and new model/selector pair", got)
	}
}

func TestLegacyRetirementIgnoresJSONFormatting(t *testing.T) {
	t.Parallel()

	before := Rule{RuleInput: rule(Selector{Kind: "endpoint", ID: model}, Method{Kind: "fixed", Schedule: jsonx.Raw(`{ "v": 1, "currency": "USD", "rates": { "output": "0.000004", "input": "0.000002" } }`)})}
	in := before.RuleInput
	in.Enabled = false
	in.Method.Schedule = jsonx.Raw(fixedUS)
	if retired, err := unchangedRetirement(before, in); err != nil || !retired {
		t.Fatal("JSON normalization prevented legacy retirement", err)
	}
}

func TestPendingAudienceTargetKeepsPublishedPrecedence(t *testing.T) {
	t.Parallel()

	f := newPrices(t)
	account := accounts.AccountID(uuid.UUID{15: 1})
	in := rule(Selector{Kind: "all"}, faced("3"))
	in.Audience = Selector{Kind: "account", ID: account.TypeID()}
	if _, err := f.create(in); err != nil {
		t.Fatal(err)
	}
	f.refresh()
	if _, err := f.create(rule(Selector{Kind: "model", ID: f.model.String()}, faced("4"))); err != nil {
		t.Fatal(err)
	}
	f.refresh()
	price := f.published().For(Audience{Account: account})
	rate, ok := price.Schedule.BaseRate(catalog.ItemInput)
	if !ok || rate.String() != "0.000003" {
		t.Fatalf("unconfirmed target changed audience price to %s", rate.String())
	}
}

func TestAudienceLocalCostGuardDoesNotFallBack(t *testing.T) {
	t.Parallel()

	f := newPrices(t)
	f.multiplier("3")
	account := accounts.AccountID(uuid.UUID{15: 1})
	in := rule(Selector{Kind: "all"}, faced("1"))
	in.Audience = Selector{Kind: "account", ID: account.TypeID()}
	if _, err := f.create(in); err != nil {
		t.Fatal(err)
	}
	f.refresh()
	model, ok := f.catalog.snapshot().Model(f.model)
	if !ok {
		t.Fatal("missing model")
	}
	local := append([]PriceCost(nil), f.costs...)
	local[0].Schedule = mustSchedule(t, `{"v":1,"currency":"XXX","rates":{"input":"0.000002","output":"0.000001"}}`)
	guarded := f.published().WithCosts(model, local)
	if guarded.For(Audience{Account: account}).Version != uuid.Nil() {
		t.Fatal("unsafe account borrowed the default price")
	}
	if guarded.For(Audience{}).Version == uuid.Nil() || !guarded.Available() {
		t.Fatal("safe default audience was stopped")
	}
}

func TestDefaultAudiencePreservesLegacyCandidate(t *testing.T) {
	t.Parallel()

	model := catalog.Model{ID: catalog.ModelID(uuid.NewV7()), Slug: "acme/legacy", Author: "acme"}
	rules := []Rule{{ID: uuid.NewV7(), RuleInput: rule(Selector{Kind: "all"}, Method{Kind: "fixed", Schedule: jsonx.Raw(fixedUS)})}}
	costs := []PriceCost{priceCost(t, model.ID, "0.0000005")}
	snap := &catalog.Snapshot{}
	legacy, err := proposePrice(defaultPricePolicy(), rules, nil, model, snap, usd, costs, fallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	all, err := proposeAudiencePrice(Selector{Kind: "all"}, defaultPricePolicy(), rules, nil, model, snap, usd, costs, fallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if all.Candidate != legacy.Candidate {
		t.Fatal("default audience changed an unchanged legacy candidate and revoked its override")
	}
}

func TestAudienceAvailabilityRequiresASelectablePrice(t *testing.T) {
	t.Parallel()

	safe := Sold{Version: uuid.NewV7()}
	table := Sold{Audiences: CompilePrices([]AudiencePrice{
		{Target: Selector{Kind: "model"}},
		{Audience: Audience{Account: accounts.AccountID(uuid.UUID{15: 1})}, Target: Selector{Kind: "all"}, Sold: safe},
	})}
	if table.Available() {
		t.Fatal("a safe layer hidden behind a blocked, more specific target made the node ready")
	}
}

// TestDefaultMultiplierScalesThePriceForAll: a tier's default multiplier is what everyone pays times it, also for a
// model a more specific rule prices for everyone, and the preview of the tier shows what serving charges it.
func TestDefaultMultiplierScalesThePriceForAll(t *testing.T) {
	t.Parallel()
	f := newPrices(t)
	tier := accounts.TierID(uuid.UUID{15: 1})
	multiplier := "1.2"
	scaled := rule(Selector{Kind: "all"}, Method{Kind: "default_multiplier", Multiplier: &multiplier})
	scaled.Audience = Selector{Kind: "tier", ID: tierID}
	for _, in := range []RuleInput{scaled, rule(Selector{Kind: "model", ID: f.model.String()}, faced("2"))} {
		if _, err := f.create(in); err != nil {
			t.Fatal(err)
		}
	}
	f.refresh()
	var preview map[catalog.ModelID]Publication
	if err := f.tx(func(tx pg.Tx) error {
		rules, err := Rules(t.Context(), tx, f.site)
		if err != nil {
			return err
		}
		rules = slices.DeleteFunc(rules, func(r Rule) bool { return !r.Audience.Prices(accounts.AccountID{}, tier) })
		preview, err = PreviewPrices(t.Context(), tx, f.site, usd, f.catalog.snapshot(), rules, []catalog.ModelID{f.model}, f.costs)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	previewed, err := catalog.ParseSchedule(preview[f.model].Schedule)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		got  catalog.Schedule
		want string
	}{
		{"everyone", f.published().For(Audience{}).Schedule, "0.000002"},
		{"the tier", f.published().For(Audience{Tier: tier}).Schedule, "0.0000024"},
		{"the tier's preview", previewed, "0.0000024"},
	} {
		if rate, ok := c.got.BaseRate(catalog.ItemInput); !ok || rate.String() != c.want {
			t.Errorf("%s pays %v a token of input, want %s", c.name, rate, c.want)
		}
	}
}

// TestTierPaysThePublishedPriceTimesItsMultiplier pins the invariant of a default multiplier: every item of the tier's
// price is the price for all accounts as published, the site floor and default prices already in it, times the
// multiplier. A price for all that waits for confirmation moves the tier only once it is published, and then in the
// same transaction as the confirmation, with no refresh between.
func TestTierPaysThePublishedPriceTimesItsMultiplier(t *testing.T) {
	t.Parallel()
	f := newPrices(t)
	f.multiplier("0.4") // under the endpoint's cost of input: the floor sets the price for all
	tier := accounts.TierID(uuid.UUID{15: 1})
	multiplier := "1.2"
	scaled := rule(Selector{Kind: "all"}, Method{Kind: "default_multiplier", Multiplier: &multiplier})
	scaled.Audience = Selector{Kind: "tier", ID: tierID}
	if _, err := f.create(scaled); err != nil {
		t.Fatal(err)
	}
	f.refresh()
	f.checkScaled(tier, multiplier, "0.0000005")
	f.multiplier("2") // four times the published price: it waits for confirmation
	f.refresh()
	f.checkScaled(tier, multiplier, "0.0000005")
	var pending Publication
	if err := f.tx(func(tx pg.Tx) error {
		_, rows, err := PriceState(t.Context(), tx, f.site)
		for _, p := range rows {
			if p.Audience.Kind == "all" || p.Audience.Kind == "" {
				pending = p
			}
		}
		return err
	}); err != nil || pending.Status != "pending" {
		t.Fatalf("the price for all is %q, %v; want it pending", pending.Status, err)
	}
	if err := f.publish(pending, true, false); err != nil {
		t.Fatal(err)
	}
	f.checkScaled(tier, multiplier, "0.000002")
}

// checkScaled fails unless the price for all has the input rate given and every item of the tier's price is that
// published price times the multiplier.
func (f *priceFixture) checkScaled(tier accounts.TierID, multiplier, input string) {
	f.t.Helper()
	sold := f.published()
	all, own := sold.For(Audience{}).Schedule, sold.For(Audience{Tier: tier}).Schedule
	if rate, ok := all.BaseRate(catalog.ItemInput); !ok || rate.String() != input {
		f.t.Fatalf("all accounts pay %v a token of input, want %s", rate, input)
	}
	want, err := all.Scaled(multiplier, usd)
	if err != nil {
		f.t.Fatal(err)
	}
	for it := range catalog.NumItems {
		wantRate, priced := want.BaseRate(it)
		got, ok := own.BaseRate(it)
		if priced != ok || priced && got.Cmp(wantRate) != 0 {
			f.t.Errorf("the tier pays %v for %s, want %v (all accounts × %s)", got, it, wantRate, multiplier)
		}
	}
}
