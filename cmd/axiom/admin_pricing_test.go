package main

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/adminapi"
)

// ruleJSON is a rule with no constraints: the member after "rule" in the body of a request.
func ruleJSON(target, audience, method string) string {
	return ruleJSONWith(target, audience, method, `{}`)
}

// ruleJSONWith is a rule with the constraints as given, as a request writes them.
func ruleJSONWith(target, audience, method, constraints string) string {
	return `{"enabled":true,"target":` + target + `,"audience":` + audience + `,"method":` + method + `,"constraints":` + constraints + `}`
}

const (
	everything = `{"kind":"all"}`
	faced      = `{"kind":"reference_multiplier","multiplier":"1.25","currency_conversion":"face"}`
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
	t.Parallel()
	f := newPricingFixture(t)
	body := `{"enabled":true,"target":{"kind":"model","id":"` + f.model + `"},"audience":{"kind":"all"},"method":{"kind":"reference_multiplier","multiplier":"1.25","currency_conversion":"face"},"constraints":{}}`
	made := f.create(body)
	if made.Rev != 0 || !made.Enabled || made.Target.Kind != "model" || *made.Target.Id != f.model || made.Audience.Id != nil ||
		*made.Method.Multiplier != "1.25" || *made.Method.CurrencyConversion != "face" ||
		!cmp.Equal(made.Constraints, adminapi.PricingConstraints{}) || time.Since(made.UpdatedAt) > time.Minute {
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

// store seeds historical pricing input directly, including unsupported target kinds.
func (f pricingFixture) store(targetKind, targetID, audienceKind, audienceID, method string) {
	f.t.Helper()
	f.storeRule(targetKind, targetID, audienceKind, audienceID, method, `{}`)
}

// storeRule is store with the constraints as given, as a release before this one could have saved them, and returns the
// rule's ID.
func (f pricingFixture) storeRule(targetKind, targetID, audienceKind, audienceID, method, constraints string) string {
	f.t.Helper()
	id := uuid.NewV7().String()
	f.run(`INSERT INTO billing.pricing_rules (id, site_id, enabled, target_kind, target_id, audience_kind, audience_id, method, constraints)
		SELECT '` + id + `', id, true, '` + targetKind + `', '` + targetID + `', '` + audienceKind + `', '` + audienceID + `', '` + method + `'::jsonb, '` + constraints + `'::jsonb
		FROM accounts.sites WHERE is_main`)
	return id
}

// TestPricingRulesAreListedInTheOrderTheyAreTried: the most specific target first, then the most specific audience.
func TestPricingRulesAreListedInTheOrderTheyAreTried(t *testing.T) {
	t.Parallel()
	f := newPricingFixture(t)
	model := `{"kind":"model","id":"` + f.model + `"}`
	for _, rule := range []string{
		ruleJSON(everything, everything, faced),
		ruleJSON(`{"kind":"author","id":"acme"}`, everything, faced),
		ruleJSON(model, everything, faced),
	} {
		f.create(rule)
	}
	// Historical rows use the same target-first, audience-second ordering.
	f.store("model", f.model, "account", f.account, faced)
	f.store("model", f.model, "tier", f.tier, faced)
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
	t.Parallel()
	f := newPricingFixture(t)
	made := f.create(ruleJSON(everything, everything, faced))
	disabled := strings.Replace(ruleJSON(everything, everything, `{"kind":"reference_multiplier","multiplier":"1.5","currency_conversion":"face"}`), `"enabled":true`, `"enabled":false`, 1)
	rec := f.change(f.s, http.MethodPut, "/pricing/rules/"+made.Id, `{"rev":0,"rule":`+disabled+`,"reason":"off for now"}`, newKey())
	expectAdmin(t, "updatePricingRule", rec, http.StatusOK, "")
	var changed adminapi.PricingRule
	decode(t, rec, &changed)
	if changed.Rev != 1 || changed.Enabled || *changed.Method.Multiplier != "1.5" || changed.Id != made.Id {
		t.Errorf("the changed rule: %+v", changed)
	}
	expectAdmin(t, "the same rev again", f.change(f.s, http.MethodPut, "/pricing/rules/"+made.Id, `{"rev":0,"rule":`+disabled+`,"reason":"r"}`, newKey()),
		http.StatusConflict, codeStaleRevision)
	if got := f.text("SELECT string_agg(rev::text || ':' || (body->'method'->>'multiplier') || ':' || reason, ',' ORDER BY rev) FROM billing.pricing_rule_versions"); got != "0:1.25:a new rule,1:1.5:off for now" {
		t.Errorf("the versions: %s", got)
	}
	f.expectAudit("pricing_rule.update", map[string]string{"reason": "off for now", "detail->>'rev'": "1"})
}

// TestPricingRuleKeys: the same request again, after a lost response, answers the same and changes nothing; the key
// of another request is refused.
func TestPricingRuleKeys(t *testing.T) {
	t.Parallel()
	f := newPricingFixture(t)
	key, body := newKey(), `{"rule":`+ruleJSON(everything, everything, faced)+`,"reason":"r"}`
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
	other := `{"rule":` + ruleJSON(`{"kind":"author","id":"acme"}`, everything, faced) + `,"reason":"r"}`
	expectAdmin(t, "the key with another rule", f.change(f.s, http.MethodPost, "/pricing/rules", other, key), http.StatusConflict, codeIdempotencyReuse)
}

// TestPricingRuleRefusals: a rule that breaks a rule of pricing rules is a 422 naming its member; a second rule of one
// target and audience is a 409; a rule that is not there is a 404; none changes anything.
func TestPricingRuleRefusals(t *testing.T) {
	t.Parallel()
	f := newPricingFixture(t)
	f.create(ruleJSON(everything, everything, faced))
	for _, c := range []struct{ name, rule, pointer string }{
		{"both ratios", ruleJSON(everything, everything, `{"kind":"cost_plus","markup_ratio":"1","margin_ratio":"0.5"}`), "/rule/method"},
		{"a negative markup", ruleJSON(`{"kind":"author","id":"acme"}`, everything, `{"kind":"cost_plus","markup_ratio":"-1"}`), "/rule/method/markup_ratio"},
		{"a multiplier of nothing", ruleJSON(`{"kind":"author","id":"acme"}`, everything, `{"kind":"reference_multiplier","multiplier":"0"}`), "/rule/method/multiplier"},
		{"a fixed price in another currency", ruleJSON(`{"kind":"author","id":"acme"}`, everything,
			`{"kind":"fixed","schedule":{"v":1,"currency":"CNY","rates":{"input":"1"}}}`), "/rule/method/schedule/currency"},
		{"a model ID that is no model's", ruleJSON(`{"kind":"model","id":"nonsense"}`, everything, faced), "/rule/target/id"},
		{"an invalid tier", ruleJSON(everything, `{"kind":"tier","id":"`+f.account+`"}`, faced), "/rule/audience/id"},
		{"an invalid account", ruleJSON(everything, `{"kind":"account","id":"`+f.tier+`"}`, faced), "/rule/audience/id"},
		// What this release prices is a model, an author or everything, by a multiplier read as the site's currency or by a
		// fixed price: the rest would be accepted and then not applied, so it is refused (docs/domains/billing.md「定价」).
		{"an endpoint", ruleJSON(`{"kind":"endpoint","id":"`+f.model+`"}`, everything, faced), "/rule/target/kind"},
		{"a group", ruleJSON(`{"kind":"group","id":"`+f.model+`"}`, everything, faced), "/rule/target/kind"},
		{"a supplier", ruleJSON(`{"kind":"supplier","id":"`+f.model+`"}`, everything, faced), "/rule/target/kind"},
		{"a source type", ruleJSON(`{"kind":"source_type","id":"relay"}`, everything, faced), "/rule/target/kind"},
		{"a multiplier by exchange", ruleJSON(`{"kind":"author","id":"acme"}`, everything,
			`{"kind":"reference_multiplier","multiplier":"2","currency_conversion":"exchange"}`), "/rule/method/currency_conversion"},
		{"a multiplier with no conversion named", ruleJSON(`{"kind":"author","id":"acme"}`, everything,
			`{"kind":"reference_multiplier","multiplier":"2"}`), "/rule/method/currency_conversion"},
		{"a model the catalog lacks", ruleJSON(`{"kind":"model","id":"0198c0a8-0000-7000-8000-000000000009"}`, everything, faced), ""},
		{"an author the catalog lacks", ruleJSON(`{"kind":"author","id":"nobody"}`, everything, faced), ""},
	} {
		rec := f.change(f.s, http.MethodPost, "/pricing/rules", `{"rule":`+c.rule+`,"reason":"r"}`, newKey())
		expectAdmin(t, c.name, rec, http.StatusUnprocessableEntity, codeUnprocessable)
		if fields := errorDetailOf(t, rec).Fields; c.pointer != "" && (fields == nil || len(*fields) == 0 || (*fields)[0].Pointer != c.pointer) {
			t.Errorf("%s: fields %v, want %s", c.name, fields, c.pointer)
		}
	}
	expectAdmin(t, "a second rule of one target and audience", f.change(f.s, http.MethodPost, "/pricing/rules",
		`{"rule":`+ruleJSON(everything, everything, faced)+`,"reason":"r"}`, newKey()), http.StatusConflict, codeConflict)
	expectAdmin(t, "a rule that is not there", f.change(f.s, http.MethodPut, "/pricing/rules/0198c0a8-0000-7000-8000-000000000009",
		`{"rev":0,"rule":`+ruleJSON(everything, everything, faced)+`,"reason":"r"}`, newKey()), http.StatusNotFound, codeNotFound)
	expectAdmin(t, "an ID that is none", f.change(f.s, http.MethodPut, "/pricing/rules/nonsense",
		`{"rev":0,"rule":`+ruleJSON(everything, everything, faced)+`,"reason":"r"}`, newKey()), http.StatusNotFound, codeNotFound)
	if got := f.text("SELECT count(*)::text FROM billing.pricing_rules") + f.auditCount("pricing_rule.create"); got != "11" {
		t.Errorf("rules and audit entries after the refusals: %s, want 1 and 1", got)
	}
}

// expectConstraintWaits checks one refusal of a constraint: 422 whose first field is the member and says it is not
// supported yet.
func expectConstraintWaits(t *testing.T, name string, rec *httptest.ResponseRecorder, pointer string) {
	t.Helper()
	expectAdmin(t, name, rec, http.StatusUnprocessableEntity, codeUnprocessable)
	fields := errorDetailOf(t, rec).Fields
	switch {
	case fields == nil || len(*fields) == 0 || (*fields)[0].Pointer != pointer:
		t.Errorf("%s: fields %v, want %s", name, fields, pointer)
	case !strings.Contains((*fields)[0].Message, "not supported yet"):
		t.Errorf("%s: %q does not say the constraint is not supported yet", name, (*fields)[0].Message)
	}
}

// TestPricingRuleConstraintsWait: this release does not apply a rule's constraints, so creating, changing or previewing
// a rule that carries any of them is refused as 422 naming the member, and nothing is stored or changed; a rule with
// none is accepted.
func TestPricingRuleConstraintsWait(t *testing.T) {
	t.Parallel()
	f := newPreview(t)
	made := f.create(ruleJSON(everything, everything, faced))
	for _, c := range []struct {
		name, constraints, pointer string
	}{
		{"a minimum margin", `{"min_margin_ratio":"0.1"}`, "/rule/constraints/min_margin_ratio"},
		{"a reference floor", `{"reference_min_ratio":"0.5"}`, "/rule/constraints/reference_min_ratio"},
		{"a reference ceiling", `{"reference_max_ratio":"3"}`, "/rule/constraints/reference_max_ratio"},
	} {
		rule := ruleJSONWith(`{"kind":"author","id":"acme"}`, everything, faced, c.constraints)
		for what, rec := range map[string]*httptest.ResponseRecorder{
			"creating":   f.change(f.s, http.MethodPost, "/pricing/rules", `{"rule":`+rule+`,"reason":"r"}`, newKey()),
			"changing":   f.change(f.s, http.MethodPut, "/pricing/rules/"+made.Id, `{"rev":0,"rule":`+rule+`,"reason":"r"}`, newKey()),
			"previewing": f.do(http.MethodPost, "/pricing/previews", `{"model_ids":["`+f.model+`"],"rule":`+rule+`}`, as(f.s)),
		} {
			expectConstraintWaits(t, what+" a rule with "+c.name, rec, c.pointer)
		}
	}
	// The contract takes a member as a decimal or leaves it out: null and an empty text never reach the rule, so they
	// are told as a malformed request, not mistaken for an absent limit.
	for _, constraints := range []string{`null`, `{"min_margin_ratio":null}`, `{"min_margin_ratio":""}`} {
		rule := ruleJSONWith(`{"kind":"author","id":"acme"}`, everything, faced, constraints)
		rec := f.do(http.MethodPost, "/pricing/previews", `{"model_ids":["`+f.model+`"],"rule":`+rule+`}`, as(f.s))
		expectAdmin(t, "previewing constraints "+constraints, rec, http.StatusBadRequest, codeInvalidRequest)
	}
	// Empty constraints are what every rule of this release has, and previewing one still accepts them.
	rows := f.preview(`{"model_ids":["` + f.model + `"],"rule":` + ruleJSON(`{"kind":"author","id":"acme"}`, everything, faced) + `}`)
	if len(rows) != 1 || rows[0].Status != adminapi.PricingPreviewRowStatusCostUnknown {
		t.Errorf("a preview with a rule of no constraints: %+v", rows)
	}
	if got := f.text("SELECT count(*)::text FROM billing.pricing_rules"); got != "1" {
		t.Errorf("%s rules after the refusals, want the one stored rule", got)
	}
	if got := f.text("SELECT count(*)::text FROM billing.pricing_rule_versions"); got != "1" {
		t.Errorf("%s versions after the refusals, want the rule's first", got)
	}
	if got := f.text("SELECT method->>'multiplier' FROM billing.pricing_rules"); got != "1.25" {
		t.Errorf("the stored rule is at %s, want 1.25: the refused changes changed nothing", got)
	}
}
