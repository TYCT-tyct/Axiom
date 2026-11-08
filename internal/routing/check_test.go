package routing_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/routing"
)

// TestStoredPresetSummary: a list's summary of a preset is its default rule's sort, balanced when it names none, and
// classes, none when it keeps every class, with whether any rule limits a price and how many overrides follow; rules
// this node cannot read have no summary.
func TestStoredPresetSummary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, rules string
		want        routing.RulesSummary
		ok          bool
	}{
		{"open", `{"default":{}}`, routing.RulesSummary{Sort: "balanced"}, true},
		{"every class", `{"default":{"classes":["*"],"sort":"latency"}}`, routing.RulesSummary{Sort: "latency"}, true},
		{
			"limited",
			`{"default":{"sort":"price","classes":["official","relay"],"verified_only":true},"rules":[{"author":"acme","max_price":{"prompt":"1"}},{"model":"acme/m","sort":"quality"}]}`,
			routing.RulesSummary{Sort: "price", Classes: []string{"official", "relay"}, VerifiedOnly: true, Priced: true, Overrides: 2},
			true,
		},
		{"a member this node does not know", `{"default":{"newer":true}}`, routing.RulesSummary{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := routing.StoredPreset{Scope: routing.ScopeAccount, Rules: jsonx.Raw(tc.rules)}
			got, ok := s.Summary()
			if ok != tc.ok || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Summary() = %+v, %v; want %+v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestCheckRules: a rule that keeps none of the endpoints in view is named, with the conditions it writes that alone
// keep none; conditions that only together keep none, or that an override inherits, name no field. A rule for an author
// or model with no endpoint in view is not judged. presetHarness's endpoints: 1 official, no retention, verified,
// scored 100 at 0.9, 1 and 2 per million; 2 relay, may train, unverified, unscored, 0.5 and 1.
func TestCheckRules(t *testing.T) {
	t.Parallel()
	def := func(fields ...string) routing.Unmatched {
		return routing.Unmatched{Rule: -1, Fields: append([]string{}, fields...)}
	}
	for _, tc := range []struct {
		name, rules string
		want        []routing.Unmatched
	}{
		{"open", `{"default":{}}`, nil},
		{"a score one endpoint has", `{"default":{"min_score":90}}`, nil},
		{"classes", `{"default":{"classes":["cloud"]}}`, []routing.Unmatched{def("classes")}},
		{"policies", `{"default":{"policies":["retained"]}}`, []routing.Unmatched{def("policies")}},
		{"score confidence", `{"default":{"min_score_confidence":0.95}}`, []routing.Unmatched{def("min_score_confidence")}},
		{"completion price", `{"default":{"max_price":{"prompt":"5","completion":"0.5"}}}`, []routing.Unmatched{def("max_price.completion")}},
		{"two that only together keep none", `{"default":{"classes":["relay"],"verified_only":true}}`, []routing.Unmatched{def()}},
		{"the platform's closed order", `{"default":{},"advanced":{"order":["g9"],"no_fallbacks":true}}`, []routing.Unmatched{def()}},
		{"a model's override", `{"default":{},"rules":[{"author":"acme","sort":"price"},{"model":"acme/m","classes":["cloud"]}]}`, []routing.Unmatched{{Rule: 1, Fields: []string{"classes"}}}},
		{"an author with no endpoint", `{"default":{},"rules":[{"author":"zeta","classes":["cloud"]}]}`, nil},
		{"inherited", `{"default":{"classes":["cloud"]},"rules":[{"author":"acme","sort":"price"}]}`, []routing.Unmatched{def("classes"), {Rule: 0, Fields: []string{}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := presetHarness(t, compiled(t, allowed))
			p := layered(routing.ScopePlatform)
			p.Currency = usd
			_, got, err := h.r.CheckRules(p, []byte(tc.rules), presetInput().Admission)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("CheckRules = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// TestCheckRulesRefusesAndWaits: rules a write refuses are refused alike, and before the first snapshot the rules are
// summarised but not judged.
func TestCheckRulesRefusesAndWaits(t *testing.T) {
	t.Parallel()
	h := presetHarness(t, compiled(t, allowed))
	if _, _, err := h.r.CheckRules(layered(routing.ScopeAccount), []byte(`{"default":{},"advanced":{}}`), presetInput().Admission); !errors.Is(err, routing.ErrRules) {
		t.Errorf("an account's advanced settings: %v, want ErrRules", err)
	}
	summary, unmatched, err := mustRouter(t).CheckRules(layered(routing.ScopeAccount), []byte(`{"default":{"classes":["cloud"]}}`), presetInput().Admission)
	if err != nil || unmatched != nil || summary.Sort != "balanced" {
		t.Errorf("without a snapshot: %+v, %+v, %v", summary, unmatched, err)
	}
}

// TestCheckRulesSeesTheLayersEndpoints: a check judges a rule against the endpoints of the layer it is for. The
// platform's view (no site, no account) has the platform's endpoints, a site's has its own too, an account's has its own
// as well; another site's and another account's endpoints answer for none of them.
func TestCheckRulesSeesTheLayersEndpoints(t *testing.T) {
	t.Parallel()
	owned := func(class routing.Class, owner routing.Owner) func(*routing.Endpoint) {
		return func(e *routing.Endpoint) { e.ModelSlug, e.Class, e.Owner = "acme/m", class, owner }
	}
	elsewhere := routing.SiteID(id(901))
	h := newHarness(t, routing.Config{}, compiled(t, allowed),
		endpoint(t, 1, owned(routing.ClassRelay, routing.Owner{})),
		endpoint(t, 2, owned(routing.ClassCloud, routing.Owner{Site: site})),
		endpoint(t, 3, owned(routing.ClassOfficial, routing.Owner{Site: elsewhere})),
		endpoint(t, 4, owned("pool", routing.Owner{Site: site, Account: acct})),
		endpoint(t, 5, owned("own", routing.Owner{Site: elsewhere, Account: routing.AccountID(id(902))})),
	)
	views := map[string]routing.Admission{
		"platform": {}, "site": {Site: site}, "account": {Site: site, Account: acct},
	}
	for _, tc := range []struct {
		class string
		keeps []string // the views in which some endpoint of the class is kept
	}{
		{"relay", []string{"platform", "site", "account"}},
		{"cloud", []string{"site", "account"}},
		{"official", nil},
		{"pool", []string{"account"}},
		{"own", nil},
	} {
		for view, a := range views {
			_, unmatched, err := h.r.CheckRules(layered(routing.ScopeAccount), []byte(`{"default":{"classes":["`+tc.class+`"]}}`), a)
			if err != nil {
				t.Fatal(err)
			}
			if kept := unmatched == nil; kept != slices.Contains(tc.keeps, view) {
				t.Errorf("%s in the %s view: unmatched %+v", tc.class, view, unmatched)
			}
		}
	}
}
