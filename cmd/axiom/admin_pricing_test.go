package main

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/adminapi"
)

// ruleJSON is a rule: the member after "rule" in the body of a request.
func ruleJSON(target, audience, method string) string {
	return `{"enabled":true,"target":` + target + `,"audience":` + audience + `,"method":` + method + `,"constraints":{}}`
}

const (
	everything = `{"kind":"all"}`
	plus25     = `{"kind":"cost_plus","markup_ratio":"0.25"}`
)

// pricingFixture is the admin with a model, a tier and an account to write rules for.
type pricingFixture struct {
	catalogFixture
	model, tier, account string
}

func newPricingFixture(t *testing.T) pricingFixture {
	t.Helper()
	f := newCatalogFixture(t)
	model := f.create("acme/alpha")
	f.addUser("alice@example.com", "")
	rec := f.do(http.MethodGet, "/tiers", "", as(f.s))
	var tiers adminapi.TierList
	decode(t, rec, &tiers)
	return pricingFixture{f, model.Id, tiers.Data[0].Id, f.accountOf("alice@example.com")}
}

// create makes a rule and returns it.
func (f pricingFixture) create(rule string) adminapi.PricingRule {
	f.t.Helper()
	rec := f.change(f.s, http.MethodPost, "/pricing/rules", `{"rule":`+rule+`,"reason":"a new rule"}`, newKey())
	expectAdmin(f.t, "createPricingRule", rec, http.StatusCreated, "")
	var made adminapi.PricingRule
	decode(f.t, rec, &made)
	return made
}

func (f pricingFixture) list() []adminapi.PricingRule {
	f.t.Helper()
	rec := f.do(http.MethodGet, "/pricing/rules", "", as(f.s))
	expectAdmin(f.t, "listPricingRules", rec, http.StatusOK, "")
	var list adminapi.PricingRuleList
	decode(f.t, rec, &list)
	return list.Data
}

// TestCreatePricingRule: a rule is made as the request says, answered with its ID and rev 0, kept as its first version,
// and audited with the reason.
func TestCreatePricingRule(t *testing.T) {
	f := newPricingFixture(t)
	body := `{"enabled":true,"target":{"kind":"model","id":"` + f.model + `"},"audience":{"kind":"all"},"method":{"kind":"cost_plus","margin_ratio":"0.2","fixed_markup":{"output":"0.000001"}},"constraints":{"min_margin_ratio":"0.1"}}`
	made := f.create(body)
	if made.Rev != 0 || !made.Enabled || made.Target.Kind != "model" || *made.Target.Id != f.model || made.Audience.Id != nil ||
		*made.Method.MarginRatio != "0.2" || *made.Method.FixedMarkup.Output != "0.000001" || *made.Constraints.MinMarginRatio != "0.1" ||
		time.Since(made.UpdatedAt) > time.Minute {
		t.Errorf("the rule: %+v", made)
	}
	if diff := cmp.Diff([]adminapi.PricingRule{made}, f.list()); diff != "" {
		t.Errorf("listPricingRules (-want +got):\n%s", diff)
	}
	f.expectAudit("pricing_rule.create", map[string]string{"reason": "a new rule", "target_id": made.Id, "detail->>'rev'": "0"})
	if got := f.text("SELECT count(*)::text FROM billing.pricing_rule_versions"); got != "1" {
		t.Errorf("%s versions, want 1", got)
	}
}

// store writes a rule for a target and an audience the way a later release will let an administrator make it, straight
// into the table, as the superuser: this release refuses to make one.
func (f pricingFixture) store(targetKind, targetID, audienceKind, audienceID, method string) {
	f.t.Helper()
	f.run(`INSERT INTO billing.pricing_rules (id, site_id, enabled, target_kind, target_id, audience_kind, audience_id, method, constraints)
		SELECT uuidv7(), id, true, '` + targetKind + `', '` + targetID + `', '` + audienceKind + `', '` + audienceID + `', '` + method + `'::jsonb, '{}'::jsonb
		FROM accounts.sites WHERE is_main`)
}

// TestPricingRulesAreListedInTheOrderTheyAreTried: the most specific target first, then the most specific audience.
func TestPricingRulesAreListedInTheOrderTheyAreTried(t *testing.T) {
	f := newPricingFixture(t)
	model := `{"kind":"model","id":"` + f.model + `"}`
	for _, rule := range []string{
		ruleJSON(everything, everything, plus25),
		ruleJSON(`{"kind":"author","id":"acme"}`, everything, plus25),
		ruleJSON(model, everything, plus25),
	} {
		f.create(rule)
	}
	// Rules for an account or a tier cannot be made yet (TestPricingRulesForOneAudienceWait), but the order they will be
	// tried in is already the stored form's, so they are written as a later release will.
	f.store("model", f.model, "account", f.account, plus25)
	f.store("model", f.model, "tier", f.tier, plus25)
	var order []string
	for _, r := range f.list() {
		order = append(order, string(r.Target.Kind)+"/"+string(r.Audience.Kind))
	}
	if want := []string{"model/account", "model/tier", "model/all", "author/all", "all/all"}; !slices.Equal(order, want) {
		t.Errorf("the order: %v, want %v", order, want)
	}
}

// TestUpdatePricingRule: a rule is replaced as of the rev it was read at, which goes up; disabling it keeps it; the old
// version stays in the history.
func TestUpdatePricingRule(t *testing.T) {
	f := newPricingFixture(t)
	made := f.create(ruleJSON(everything, everything, plus25))
	disabled := strings.Replace(ruleJSON(everything, everything, `{"kind":"cost_plus","markup_ratio":"0.5"}`), `"enabled":true`, `"enabled":false`, 1)
	rec := f.change(f.s, http.MethodPut, "/pricing/rules/"+made.Id, `{"rev":0,"rule":`+disabled+`,"reason":"off for now"}`, newKey())
	expectAdmin(t, "updatePricingRule", rec, http.StatusOK, "")
	var changed adminapi.PricingRule
	decode(t, rec, &changed)
	if changed.Rev != 1 || changed.Enabled || *changed.Method.MarkupRatio != "0.5" || changed.Id != made.Id {
		t.Errorf("the changed rule: %+v", changed)
	}
	expectAdmin(t, "the same rev again", f.change(f.s, http.MethodPut, "/pricing/rules/"+made.Id, `{"rev":0,"rule":`+disabled+`,"reason":"r"}`, newKey()),
		http.StatusConflict, codeStaleRevision)
	if got := f.text("SELECT string_agg(rev::text || ':' || (body->'method'->>'markup_ratio') || ':' || reason, ',' ORDER BY rev) FROM billing.pricing_rule_versions"); got != "0:0.25:a new rule,1:0.5:off for now" {
		t.Errorf("the versions: %s", got)
	}
	f.expectAudit("pricing_rule.update", map[string]string{"reason": "off for now", "detail->>'rev'": "1"})
}

// TestPricingRuleKeys: the same request again, after a lost response, answers the same and changes nothing; the key
// of another request is refused.
func TestPricingRuleKeys(t *testing.T) {
	f := newPricingFixture(t)
	key, body := newKey(), `{"rule":`+ruleJSON(everything, everything, plus25)+`,"reason":"r"}`
	rec := f.change(f.s, http.MethodPost, "/pricing/rules", body, key)
	expectAdmin(t, "createPricingRule", rec, http.StatusCreated, "")
	var first, again adminapi.PricingRule
	decode(t, rec, &first)
	rec = f.change(f.s, http.MethodPost, "/pricing/rules", body, key)
	decode(t, rec, &again)
	if diff := cmp.Diff(first, again); diff != "" || rec.Code != http.StatusCreated || rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("the replay (-want +got) %d replayed=%q:\n%s", rec.Code, rec.Header().Get("Idempotency-Replayed"), diff)
	}
	if got := f.text("SELECT count(*)::text FROM billing.pricing_rules"); got != "1" {
		t.Errorf("%s rules, want 1", got)
	}
	other := `{"rule":` + ruleJSON(`{"kind":"author","id":"acme"}`, everything, plus25) + `,"reason":"r"}`
	expectAdmin(t, "the key with another rule", f.change(f.s, http.MethodPost, "/pricing/rules", other, key), http.StatusConflict, codeIdempotencyReuse)
}

// TestPricingRuleRefusals: a rule that breaks a rule of pricing rules is a 422 naming its member; a second rule of one
// target and audience is a 409; a rule that is not there is a 404; none changes anything.
func TestPricingRuleRefusals(t *testing.T) {
	f := newPricingFixture(t)
	f.create(ruleJSON(everything, everything, plus25))
	for _, c := range []struct{ name, rule, pointer string }{
		{"both ratios", ruleJSON(everything, everything, `{"kind":"cost_plus","markup_ratio":"1","margin_ratio":"0.5"}`), "/rule/method"},
		{"a negative markup", ruleJSON(`{"kind":"author","id":"acme"}`, everything, `{"kind":"cost_plus","markup_ratio":"-1"}`), "/rule/method/markup_ratio"},
		{"a multiplier of nothing", ruleJSON(`{"kind":"author","id":"acme"}`, everything, `{"kind":"reference_multiplier","multiplier":"0"}`), "/rule/method/multiplier"},
		{"a fixed price in another currency", ruleJSON(`{"kind":"author","id":"acme"}`, everything,
			`{"kind":"fixed","schedule":{"v":1,"currency":"CNY","rates":{"input":"1"}}}`), "/rule/method/schedule/currency"},
		{"a model ID that is no model's", ruleJSON(`{"kind":"model","id":"nonsense"}`, everything, plus25), "/rule/target/id"},
		{"a tier", ruleJSON(everything, `{"kind":"tier","id":"`+f.tier+`"}`, plus25), "/rule/audience/kind"},
		{"an account", ruleJSON(everything, `{"kind":"account","id":"`+f.account+`"}`, plus25), "/rule/audience/kind"},
		{"a model the catalog lacks", ruleJSON(`{"kind":"model","id":"0198c0a8-0000-7000-8000-000000000009"}`, everything, plus25), ""},
		{"an author the catalog lacks", ruleJSON(`{"kind":"author","id":"nobody"}`, everything, plus25), ""},
	} {
		rec := f.change(f.s, http.MethodPost, "/pricing/rules", `{"rule":`+c.rule+`,"reason":"r"}`, newKey())
		expectAdmin(t, c.name, rec, http.StatusUnprocessableEntity, codeUnprocessable)
		if fields := errorDetailOf(t, rec).Fields; c.pointer != "" && (fields == nil || len(*fields) == 0 || (*fields)[0].Pointer != c.pointer) {
			t.Errorf("%s: fields %v, want %s", c.name, fields, c.pointer)
		}
	}
	expectAdmin(t, "a second rule of one target and audience", f.change(f.s, http.MethodPost, "/pricing/rules",
		`{"rule":`+ruleJSON(everything, everything, plus25)+`,"reason":"r"}`, newKey()), http.StatusConflict, codeConflict)
	expectAdmin(t, "a rule that is not there", f.change(f.s, http.MethodPut, "/pricing/rules/0198c0a8-0000-7000-8000-000000000009",
		`{"rev":0,"rule":`+ruleJSON(everything, everything, plus25)+`,"reason":"r"}`, newKey()), http.StatusNotFound, codeNotFound)
	expectAdmin(t, "an ID that is none", f.change(f.s, http.MethodPut, "/pricing/rules/nonsense",
		`{"rev":0,"rule":`+ruleJSON(everything, everything, plus25)+`,"reason":"r"}`, newKey()), http.StatusNotFound, codeNotFound)
	if got := f.text("SELECT count(*)::text FROM billing.pricing_rules") + f.auditCount("pricing_rule.create"); got != "11" {
		t.Errorf("rules and audit entries after the refusals: %s, want 1 and 1", got)
	}
}
