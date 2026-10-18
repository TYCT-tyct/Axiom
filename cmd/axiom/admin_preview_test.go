package main

import (
	"net/http"
	"testing"

	"axiom/internal/platform/adminapi"
)

// preview asks for the prices of models, with an optional rule and replacements, and returns the rows.
func (f pricingFixture) preview(body string) []adminapi.PricingPreviewRow {
	f.t.Helper()
	rec := f.do(http.MethodPost, "/pricing/previews", body, as(f.s))
	expectAdmin(f.t, "previewPricing", rec, http.StatusOK, "")
	var preview adminapi.PricingPreview
	decode(f.t, rec, &preview)
	return preview.Data
}

// newPreview is the admin with a model on sale, and a tier and an account of the site, which a preview prices for.
func newPreview(t *testing.T) pricingFixture {
	t.Helper()
	f := newCatalogFixture(t)
	model := f.listed("acme/alpha")
	f.addUser("alice@example.com", "")
	rec := f.do(http.MethodGet, "/tiers", "", as(f.s))
	var tiers adminapi.TierList
	decode(t, rec, &tiers)
	return pricingFixture{f, model.Id, tiers.Data[0].Id, f.accountOf("alice@example.com")}
}

// listed is a model on sale: a preview reads the catalog's snapshot, which holds the listed models and their prices,
// so a draft would be no model at all to it.
func (f catalogFixture) listed(slug string) adminapi.Model {
	f.t.Helper()
	rec := f.setStatus(f.ready(slug), "listed")
	expectAdmin(f.t, "listing "+slug, rec, http.StatusOK, "")
	var model adminapi.Model
	decode(f.t, rec, &model)
	return model
}

// timesTwo is a sale price: the reference price times 2, read at face.
const timesTwo = `{"kind":"reference_multiplier","multiplier":"2","currency_conversion":"face"}`

// TestPreviewPricing: a rule that is stored, or one tried for the preview alone, says what each model would sell for,
// and a model no rule prices has no price: M1 never falls back to the reference price. Nothing is stored.
func TestPreviewPricing(t *testing.T) {
	f := newPreview(t)
	model := `{"kind":"model","id":"` + f.model + `"}`
	f.create(ruleJSON(model, everything, timesTwo))
	rows := f.preview(`{"model_ids":["` + f.model + `"]}`)
	if len(rows) != 1 || rows[0].Status != adminapi.Priced || rows[0].Sale == nil || rows[0].Sale.Rates.Input == nil || *rows[0].Sale.Rates.Input != "0.000002" || rows[0].Slug != "acme/alpha" {
		t.Fatalf("the preview: %+v", rows)
	}
	// A rule for everything that the preview adds prices a model nothing stored prices; the stored rule still wins for
	// the model it names.
	other := f.listed("acme/beta")
	rows = f.preview(`{"model_ids":["` + f.model + `","` + other.Id + `"],"rule":` + ruleJSON(everything, everything, timesTwo) + `}`)
	sale := map[string]string{}
	for _, row := range rows {
		sale[row.ModelId] = priced(row.Sale.Rates.Input)
	}
	if want := map[string]string{f.model: "0.000002", other.Id: "0.000002"}; len(sale) != 2 || sale[f.model] != want[f.model] || sale[other.Id] != want[other.Id] {
		t.Errorf("the preview with a rule: %v, want %v", sale, want)
	}
	// A model no rule prices is unpriced, and says so with no price at all.
	rows = f.preview(`{"model_ids":["` + other.Id + `"]}`)
	if len(rows) != 1 || rows[0].Status != adminapi.Unpriced || rows[0].Sale != nil || rows[0].Cost != nil || rows[0].MarginRatio != nil {
		t.Errorf("a model no rule prices: %+v", rows)
	}
	if got := f.text("SELECT count(*)::text FROM billing.pricing_rules"); got != "1" {
		t.Errorf("%s rules after the previews, want the one stored rule: a preview stores nothing", got)
	}
}

// TestPreviewPricingSubstitutesARule: the rule in the request takes the place of replaces_rule_id, so what a change
// would do is shown before it is made.
func TestPreviewPricingSubstitutesARule(t *testing.T) {
	f := newPreview(t)
	made := f.create(ruleJSON(everything, everything, timesTwo))
	body := `{"model_ids":["` + f.model + `"],"replaces_rule_id":"` + made.Id + `","rule":` +
		ruleJSON(everything, everything, `{"kind":"reference_multiplier","multiplier":"3","currency_conversion":"face"}`) + `}`
	rows := f.preview(body)
	if len(rows) != 1 || rows[0].Sale == nil || priced(rows[0].Sale.Rates.Input) != "0.000003" {
		t.Fatalf("the preview with a replacement: %+v", rows)
	}
	if got := f.text("SELECT (method->>'multiplier') FROM billing.pricing_rules"); got != "2" {
		t.Errorf("the stored rule is at %s, want 2: the preview changed nothing", got)
	}
}

// TestPreviewPricingRefusals: a request that names a model the catalog does not hold, a tier or an account the site
// lacks, or a rule that breaks a rule of rules, is refused and previews nothing.
func TestPreviewPricingRefusals(t *testing.T) {
	f := newPreview(t)
	f.create(ruleJSON(everything, everything, timesTwo))
	for _, c := range []struct {
		name, body string
		status     int
		code       errorCode
	}{
		{"a model the catalog lacks", `{"model_ids":["0198c0a8-0000-7000-8000-000000000009"]}`, http.StatusNotFound, codeNotFound},
		{"a model ID that is not a UUID, which the contract refuses first", `{"model_ids":["nonsense"]}`, http.StatusBadRequest, codeInvalidRequest},
		{"a tier and an account", `{"model_ids":["` + f.model + `"],"tier_id":"` + f.tier + `","account_id":"` + f.account + `"}`, http.StatusBadRequest, codeInvalidRequest},
		{"an account the site lacks", `{"model_ids":["` + f.model + `"],"account_id":"acct_00000000000000000000000000"}`, http.StatusNotFound, codeNotFound},
		{"a tier the site lacks", `{"model_ids":["` + f.model + `"],"tier_id":"tier_00000000000000000000000001"}`, http.StatusUnprocessableEntity, codeUnprocessable},
		{"a rule that breaks a rule of rules", `{"model_ids":["` + f.model + `"],"rule":` + ruleJSON(everything, everything, `{"kind":"cost_plus"}`) + `}`, http.StatusUnprocessableEntity, codeUnprocessable},
		{"a replacement that is not there", `{"model_ids":["` + f.model + `"],"replaces_rule_id":"0198c0a8-0000-7000-8000-000000000009","rule":` + ruleJSON(everything, everything, timesTwo) + `}`, http.StatusNotFound, codeNotFound},
		{"a replacement without a rule", `{"model_ids":["` + f.model + `"],"replaces_rule_id":"0198c0a8-0000-7000-8000-000000000009"}`, http.StatusBadRequest, codeInvalidRequest},
	} {
		rec := f.do(http.MethodPost, "/pricing/previews", c.body, as(f.s))
		expectAdmin(t, c.name, rec, c.status, c.code)
	}
}

// TestPreviewPricingAudience: a rule for a tier or an account is shown only to those it prices, so the preview of one
// tier does not show another tier's price, and a preview that names no audience shows the site's default tier's.
func TestPreviewPricingAudience(t *testing.T) {
	f := newPreview(t)
	f.run(`INSERT INTO accounts.tiers (id, site_id, name) SELECT uuidv7(), id, 'Beta' FROM accounts.sites WHERE is_main`)
	rec := f.do(http.MethodGet, "/tiers", "", as(f.s))
	var tiers adminapi.TierList
	decode(t, rec, &tiers)
	var other string
	for _, tier := range tiers.Data {
		if tier.Id != f.tier {
			other = tier.Id
		}
	}
	if other == "" {
		t.Fatalf("the tiers: %+v; want one besides %s", tiers.Data, f.tier)
	}
	f.create(ruleJSON(everything, `{"kind":"tier","id":"`+other+`"}`, timesTwo))
	f.create(ruleJSON(everything, `{"kind":"tier","id":"`+f.tier+`"}`,
		`{"kind":"reference_multiplier","multiplier":"5","currency_conversion":"face"}`))
	f.create(ruleJSON(everything, everything, `{"kind":"reference_multiplier","multiplier":"4","currency_conversion":"face"}`))
	in := func(audience string) string {
		rows := f.preview(`{"model_ids":["` + f.model + `"]` + audience + `}`)
		if len(rows) != 1 || rows[0].Sale == nil {
			return ""
		}
		return priced(rows[0].Sale.Rates.Input)
	}
	if got, want := in(`,"tier_id":"`+f.tier+`"`), "0.000005"; got != want {
		t.Errorf("the default tier pays %s, want its own %s", got, want)
	}
	if got, want := in(`,"tier_id":"`+other+`"`), "0.000002"; got != want {
		t.Errorf("the other tier pays %s, want its own %s", got, want)
	}
	// No audience is the site's default tier, not the rule for everyone else.
	if got, want := in(""), "0.000005"; got != want {
		t.Errorf("a preview of no audience pays %s, want the default tier's %s", got, want)
	}
	if got, want := in(`,"account_id":"`+f.account+`"`), "0.000005"; got != want {
		t.Errorf("the account pays %s, want its tier's %s", got, want)
	}
}

// priced is a price of a preview row as the contract gives it, for a test to compare.
func priced(p *adminapi.UnitPrice) string {
	if p == nil {
		return ""
	}
	return *p
}
