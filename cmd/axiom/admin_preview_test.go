package main

import (
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/pg"
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

// timesThree and timesFour are further sale prices, the reference price times them at face.
const (
	timesThree = `{"kind":"reference_multiplier","multiplier":"3","currency_conversion":"face"}`
	timesFour  = `{"kind":"reference_multiplier","multiplier":"4","currency_conversion":"face"}`
)

// TestPreviewPricing: a rule that is stored says what the model it names would sell for, and asking for the price
// stores nothing.
//
// Of the contract's four statuses, only priced and unpriced have a producer in M1: below_floor and cost_unknown both
// compare a sale price with an endpoint's cost, and an endpoint has one from routing in M2 (m1-plan.md §4.2). No test
// can show a row of either.
func TestPreviewPricing(t *testing.T) {
	t.Parallel()
	f := newPreview(t)
	f.create(ruleJSON(`{"kind":"model","id":"`+f.model+`"}`, everything, timesTwo))
	rows := f.preview(`{"model_ids":["` + f.model + `"]}`)
	if len(rows) != 1 || rows[0].Status != adminapi.PricingPreviewRowStatusCostUnknown {
		t.Fatalf("the preview: %+v", rows)
	}
	if got, slug := saleOf(rows[0]), rows[0].Slug; got != "0.000002" || slug != "acme/alpha" {
		t.Errorf("the model priced at %q for %q, want 0.000002 for acme/alpha", got, slug)
	}
	if got := f.text("SELECT count(*)::text FROM billing.pricing_rules"); got != "1" {
		t.Errorf("%s rules after the preview, want the one stored rule: a preview stores nothing", got)
	}
}

// TestPreviewPricingRefusesAStoredConstraint: a rule stored before this release with a constraint does not price the
// model it matches: the model is unpriced instead of falling to the rule for everything, whichever order the models are
// asked in, and the reason names the rule and the model in the log.
func TestPreviewPricingRefusesAStoredConstraint(t *testing.T) {
	t.Parallel()
	f := newPreview(t)
	other := f.listed("acme/beta")
	ruleID := f.storeRule("model", f.model, "all", "", timesTwo, `{"min_margin_ratio":"0.1"}`)
	f.create(ruleJSON(everything, everything, timesFour))
	want := map[string]adminapi.PricingPreviewRowStatus{f.model: adminapi.PricingPreviewRowStatusUnpriced, other.Id: adminapi.PricingPreviewRowStatusCostUnknown}
	for _, ids := range [][2]string{{f.model, other.Id}, {other.Id, f.model}} {
		rows := f.preview(`{"model_ids":["` + ids[0] + `","` + ids[1] + `"]}`)
		got, sale := map[string]adminapi.PricingPreviewRowStatus{}, ""
		for _, row := range rows {
			got[row.ModelId] = row.Status
			if row.ModelId == other.Id {
				sale = saleOf(row)
			}
		}
		if !maps.Equal(got, want) || sale != "0.000004" {
			t.Errorf("the preview of %v: %v at %q, want %v with the other model at 0.000004", ids, got, sale, want)
		}
	}
	if logs := f.logs.String(); !strings.Contains(logs, ruleID) || !strings.Contains(logs, f.model) {
		t.Errorf("the refusal was not logged with the rule and the model: %s", logs)
	}
	if got := f.text(`SELECT rev::text || ':' || enabled::text || ':' || (constraints->>'min_margin_ratio')
		FROM billing.pricing_rules WHERE id = '` + ruleID + `'`); got != "0:true:0.1" {
		t.Errorf("the stored rule after the previews is %s, want 0:true:0.1: it was neither rewritten nor disabled", got)
	}
}

// TestPreviewPricingTriesARule: a rule the request carries prices a model nothing stored prices, and leaves the stored
// rule to the model it names.
func TestPreviewPricingTriesARule(t *testing.T) {
	t.Parallel()
	f := newPreview(t)
	f.create(ruleJSON(`{"kind":"model","id":"`+f.model+`"}`, everything, timesTwo))
	other := f.listed("acme/beta")
	rows := f.preview(`{"model_ids":["` + f.model + `","` + other.Id + `"],"rule":` + ruleJSON(everything, everything, timesTwo) + `}`)
	sale := map[string]string{}
	for _, row := range rows {
		sale[row.ModelId] = saleOf(row)
	}
	want := map[string]string{f.model: "0.000002", other.Id: "0.000002"}
	if len(sale) != 2 || !maps.Equal(sale, want) {
		t.Errorf("the preview with a rule: %v, want %v", sale, want)
	}
}

// TestPreviewPricingLeavesAModelUnpriced: a model no rule prices has no price at all, never its reference price.
func TestPreviewPricingLeavesAModelUnpriced(t *testing.T) {
	t.Parallel()
	f := newPreview(t)
	f.create(ruleJSON(`{"kind":"model","id":"`+f.model+`"}`, everything, timesTwo))
	other := f.listed("acme/beta")
	rows := f.preview(`{"model_ids":["` + other.Id + `"]}`)
	if len(rows) != 1 || rows[0].Status != adminapi.PricingPreviewRowStatusUnpriced || rows[0].Sale != nil || rows[0].Cost != nil || rows[0].MarginRatio != nil {
		t.Errorf("a model no rule prices: %+v", rows)
	}
}

// TestPreviewPricingSubstitutesARule: the rule in the request takes the place of replaces_rule_id, so what a change
// would do is shown before it is made.
func TestPreviewPricingSubstitutesARule(t *testing.T) {
	t.Parallel()
	f := newPreview(t)
	made := f.create(ruleJSON(everything, everything, timesTwo))
	body := `{"model_ids":["` + f.model + `"],"replaces_rule_id":"` + made.Id + `","rule":` +
		ruleJSON(everything, everything, `{"kind":"reference_multiplier","multiplier":"3","currency_conversion":"face"}`) + `}`
	rows := f.preview(body)
	if len(rows) != 1 || saleOf(rows[0]) != "0.000003" {
		t.Fatalf("the preview with a replacement: %+v", rows)
	}
	if got := f.text("SELECT (method->>'multiplier') FROM billing.pricing_rules"); got != "2" {
		t.Errorf("the stored rule is at %s, want 2: the preview changed nothing", got)
	}
}

// TestPreviewPricingRefusals: a request that names a model the catalog does not hold, a tier or an account the site
// lacks, or a rule that breaks a rule of rules, is refused and previews nothing.
func TestPreviewPricingRefusals(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	f.store("all", "", "tier", other, timesTwo)
	f.store("all", "", "tier", f.tier, `{"kind":"reference_multiplier","multiplier":"5","currency_conversion":"face"}`)
	f.create(ruleJSON(everything, everything, `{"kind":"reference_multiplier","multiplier":"4","currency_conversion":"face"}`))
	in := func(audience string) string {
		rows := f.preview(`{"model_ids":["` + f.model + `"]` + audience + `}`)
		if len(rows) != 1 {
			return ""
		}
		return saleOf(rows[0])
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

// TestPreviewPricingPlacesAReplacementByItsTarget: a rule put in place of one for everything is tried as the rule it
// is, where its own target and audience put it, not where the rule it replaces stood: a rule for a model decides for
// that model, and the rule for its author, which stood before the replaced rule, does not.
func TestPreviewPricingPlacesAReplacementByItsTarget(t *testing.T) {
	t.Parallel()
	f := newPreview(t)
	other := f.listed("acme/beta")
	f.create(ruleJSON(`{"kind":"author","id":"acme"}`, everything, timesTwo))
	all := f.create(ruleJSON(everything, everything, timesFour))
	body := `{"model_ids":["` + f.model + `","` + other.Id + `"],"replaces_rule_id":"` + all.Id + `","rule":` +
		ruleJSON(`{"kind":"model","id":"`+f.model+`"}`, everything, timesThree) + `}`
	sale := map[string]string{}
	for _, row := range f.preview(body) {
		sale[row.ModelId] = saleOf(row)
	}
	if want := map[string]string{f.model: "0.000003", other.Id: "0.000002"}; !maps.Equal(sale, want) {
		t.Errorf("the preview with a replacement for everything: %v, want %v", sale, want)
	}
}

// TestPreviewPricingRefusesARuleThatCannotDecide: a rule for a target and audience the site already prices is refused
// as creating it would be, and a replacement that would tie another rule names replaces_rule_id, the member that
// changes that rule instead.
func TestPreviewPricingRefusesARuleThatCannotDecide(t *testing.T) {
	t.Parallel()
	f := newPreview(t)
	f.create(ruleJSON(everything, everything, timesTwo))
	mine := f.create(ruleJSON(`{"kind":"model","id":"`+f.model+`"}`, everything, timesThree))
	tie := ruleJSON(everything, everything, timesFour)
	expectAdmin(t, "a rule for everything, which the site has",
		f.do(http.MethodPost, "/pricing/previews", `{"model_ids":["`+f.model+`"],"rule":`+tie+`}`, as(f.s)),
		http.StatusConflict, codeConflict)
	rec := f.do(http.MethodPost, "/pricing/previews",
		`{"model_ids":["`+f.model+`"],"replaces_rule_id":"`+mine.Id+`","rule":`+tie+`}`, as(f.s))
	expectAdmin(t, "a replacement that ties the rule for everything", rec, http.StatusUnprocessableEntity, codeUnprocessable)
	if body := rec.Body.String(); !strings.Contains(body, `"/replaces_rule_id"`) {
		t.Errorf("the refusal does not name replaces_rule_id: %s", body)
	}
	if got := f.text("SELECT count(*)::text FROM billing.pricing_rules"); got != "2" {
		t.Errorf("%s rules after two refused previews, want the 2 stored ones", got)
	}
}

// saleOf is one row's input price as the contract gives it, for a test to compare; empty when the row has no price.
func saleOf(row adminapi.PricingPreviewRow) string {
	if row.Sale == nil || row.Sale.Rates.Input == nil {
		return ""
	}
	return *row.Sale.Rates.Input
}

// TestPricingRulesForOneAudience supports creation, editing, preview and retirement through the real admin boundary.
func TestPricingRulesForOneAudience(t *testing.T) {
	t.Parallel()
	f := newPreview(t)
	for _, audience := range []string{`{"kind":"tier","id":"` + f.tier + `"}`, `{"kind":"account","id":"` + f.account + `"}`} {
		made := f.create(ruleJSON(everything, audience, timesTwo))
		body := ruleJSON(everything, audience, timesThree)
		rec := f.do(http.MethodPost, "/pricing/previews", `{"model_ids":["`+f.model+`"],"rule":`+body+`,"replaces_rule_id":"`+made.Id+`"}`, as(f.s))
		expectAdmin(t, "audience preview", rec, http.StatusOK, "")
		rec = f.change(f.s, http.MethodPut, "/pricing/rules/"+made.Id, `{"rev":0,"rule":`+body+`,"reason":"edit audience"}`, newKey())
		expectAdmin(t, "audience edit", rec, http.StatusOK, "")
		body = strings.Replace(body, `"enabled":true`, `"enabled":false`, 1)
		rec = f.change(f.s, http.MethodPut, "/pricing/rules/"+made.Id, `{"rev":1,"rule":`+body+`,"reason":"retire audience"}`, newKey())
		expectAdmin(t, "audience retirement", rec, http.StatusOK, "")
	}
}

// TestFileSearchRuleWaitsForEveryNode: a sale price naming file_search is refused while the catalog's switch is off, in
// both a fixed price list and a fixed markup, so a stored rule cannot make an older node's price refresh fail.
func TestFileSearchRuleWaitsForEveryNode(t *testing.T) {
	t.Parallel()
	f := newPreview(t)
	for _, tc := range []struct {
		name, method, field string
	}{
		{"a fixed price", `{"kind":"fixed","schedule":{"v":1,"currency":"USD","rates":{"input":"0.000001","file_search":"0.0025"}}}`, "/rule/method/schedule"},
		{"a fixed markup", `{"kind":"cost_plus","markup_ratio":"0.1","fixed_markup":{"file_search":"0.0025"}}`, "/rule/method/fixed_markup"},
	} {
		rec := f.change(f.s, http.MethodPost, "/pricing/rules",
			`{"rule":`+ruleJSON(everything, everything, tc.method)+`,"reason":"a new rule"}`, newKey())
		expectAdmin(t, tc.name, rec, http.StatusUnprocessableEntity, codeUnprocessable)
		if body := rec.Body.String(); !strings.Contains(body, tc.field) {
			t.Errorf("%s: the refusal does not name %s: %s", tc.name, tc.field, body)
		}
	}
	if got := f.text(`SELECT count(*)::text FROM billing.pricing_rules
		WHERE method->'schedule'->'rates' ? 'file_search' OR method->'fixed_markup' ? 'file_search'`); got != "0" {
		t.Errorf("%s rules naming file_search after the refusals, want none", got)
	}
}

func TestLegacyRuleRetirementAPI(t *testing.T) {
	t.Parallel()
	f := newPreview(t)
	target := `{"kind":"endpoint","id":"` + f.model + `"}`
	id := f.storeRule("endpoint", f.model, "all", "", timesTwo, `{"min_margin_ratio":"0.1"}`)
	body := strings.Replace(ruleJSONWith(target, everything, timesTwo, `{"min_margin_ratio":"0.1"}`), `"enabled":true`, `"enabled":false`, 1)
	rec := f.change(f.s, http.MethodPut, "/pricing/rules/"+id, `{"rev":0,"rule":`+body+`,"reason":"retire legacy"}`, newKey())
	expectAdmin(t, "retire unsupported legacy input", rec, http.StatusOK, "")
	body = strings.Replace(body, `"enabled":false`, `"enabled":true`, 1)
	rec = f.change(f.s, http.MethodPut, "/pricing/rules/"+id, `{"rev":1,"rule":`+body+`,"reason":"unsafe enable"}`, newKey())
	expectAdmin(t, "reject unsupported enable", rec, http.StatusUnprocessableEntity, codeUnprocessable)
}

func TestPricingRuleEditValidatesDisabledChanges(t *testing.T) {
	t.Parallel()
	f := newPreview(t)
	id := f.storeRule("all", "", "all", "", timesTwo, `{}`)
	foreignSite, foreignTier, foreignAccount := uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
	owner, err := accounts.ParseAccountID(f.account)
	if err != nil {
		t.Fatal(err)
	}
	f.run("BEGIN", fmt.Sprintf(`INSERT INTO accounts.sites (id,parent_site_id,owner_account_id,name,status,key_prefix,registration,default_tier_id)
      SELECT '%s',id,'%s','Other','active','sk-other-','closed','%s' FROM accounts.sites WHERE is_main`, foreignSite, uuid.UUID(owner), foreignTier),
		fmt.Sprintf(`INSERT INTO accounts.tiers(id,site_id,name) VALUES ('%s','%s','Other')`, foreignTier, foreignSite),
		fmt.Sprintf(`INSERT INTO accounts.accounts(id,site_id,kind,name,status,tier_id) VALUES ('%s','%s','personal','Other','active','%s')`, foreignAccount, foreignSite, foreignTier), "COMMIT")
	for _, enabled := range []bool{false, true} {
		for _, subject := range []struct{ target, audience, pointer string }{
			{everything, fmt.Sprintf(`{"kind":"account","id":%q}`, accounts.AccountID(foreignAccount).TypeID()), "/rule/audience/id"},
			{everything, `{"kind":"account","id":"acct_00000000000000000000000001"}`, "/rule/audience/id"},
			{everything, `{"kind":"tier","id":"tier_00000000000000000000000001"}`, "/rule/audience/id"},
			{`{"kind":"model","id":"00000000-0000-4000-8000-000000000001"}`, everything, ""},
		} {
			body := strings.Replace(ruleJSON(subject.target, subject.audience, timesTwo), `"enabled":true`, fmt.Sprintf(`"enabled":%t`, enabled), 1)
			rec := f.change(f.s, http.MethodPut, "/pricing/rules/"+id, `{"rev":0,"rule":`+body+`,"reason":"change subjects"}`, newKey())
			expectAdmin(t, "validate changed subjects", rec, http.StatusUnprocessableEntity, codeUnprocessable)
			if subject.pointer != "" && !strings.Contains(rec.Body.String(), subject.pointer) {
				t.Fatal(rec.Body.String())
			}
			rec = f.change(f.s, http.MethodPost, "/pricing/rules", `{"rule":`+body+`,"reason":"create subjects"}`, newKey())
			expectAdmin(t, "validate created subjects", rec, http.StatusUnprocessableEntity, codeUnprocessable)
			if subject.pointer != "" && !strings.Contains(rec.Body.String(), subject.pointer) {
				t.Fatal(rec.Body.String())
			}
		}
	}
}

// TestPreviewEndpointRows: a cost-plus proposal shows one row per endpoint, each with its own sale, cost, floor status
// and margin. The model-level envelope would show only the dearest endpoint and hide the other's price.
//
//nolint:cyclop // one cost-plus preview through every endpoint row
func TestPreviewEndpointRows(t *testing.T) {
	t.Parallel()
	f := newPreview(t)
	var snap *catalog.Snapshot
	var err error
	if err := f.db.ReadTx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) (err error) {
		snap, err = catalog.ReadSnapshot(t.Context(), tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	model, err := modelIDOf(f.model)
	if err != nil {
		t.Fatal(err)
	}
	schedule := func(input, output string) catalog.Schedule {
		s, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"` + input + `","output":"` + output + `"}}`))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	first, second := uuid.MustParse("00000000-0000-7000-8000-000000000001"), uuid.MustParse("00000000-0000-7000-8000-000000000002")
	p := billing.Publication{
		Model:              model,
		Endpoints:          billing.EndpointPrices{first: schedule("0.0000015", "0.000003"), second: schedule("0.000004", "0.000008")},
		RequestedEndpoints: billing.EndpointPrices{first: schedule("0.0000012", "0.0000024"), second: schedule("0.000004", "0.000008")},
		Costs:              billing.EndpointPrices{first: schedule("0.000001", "0.000002"), second: schedule("0.000002", "0.000004")},
		Floors:             billing.EndpointPrices{first: schedule("0.0000015", "0.000003"), second: schedule("0.000003", "0.000006")},
	}
	rows, err := previewRows(adminapi.PricingPreviewRequest{ModelIds: []string{f.model}}, snap, []catalog.ModelID{model}, map[catalog.ModelID]billing.Publication{model: p}, nil, audience{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Data) != 2 || rows.Data[0].EndpointId == nil || *rows.Data[0].EndpointId != first.String() ||
		rows.Data[1].EndpointId == nil || *rows.Data[1].EndpointId != second.String() {
		t.Fatalf("the rows: %+v", rows.Data)
	}
	if rows.Data[0].Status != adminapi.PricingPreviewRowStatusBelowFloor || rows.Data[1].Status != adminapi.PricingPreviewRowStatusPriced {
		t.Errorf("statuses %s, %s", rows.Data[0].Status, rows.Data[1].Status)
	}
	if rows.Data[0].MarginRatio == nil || *rows.Data[0].MarginRatio != "0.333333333333333" {
		t.Errorf("the first endpoint's margin: %v", rows.Data[0].MarginRatio)
	}
	if rows.Data[1].MarginRatio == nil || *rows.Data[1].MarginRatio != "0.5" {
		t.Errorf("the second endpoint's margin: %v", rows.Data[1].MarginRatio)
	}
}
