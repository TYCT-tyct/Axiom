package routing_test

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/routing"
)

var (
	work    = routing.WorkspaceID(id(900))
	usd     = mustCurrency("USD")
	allowed = `{"default":{"classes":["*"],"policies":["*"]}}`
)

func mustCurrency(code string) platform.Currency {
	c, err := platform.ParseCurrency(code)
	if err != nil {
		panic(err)
	}
	return c
}

// layered is a preset of the scope, owned by the test's site, account and workspace as far as the scope reaches.
func layered(scope routing.PresetScope) routing.Preset {
	p := routing.Preset{ID: pre, Scope: scope}
	if scope >= routing.ScopeSite {
		p.Site = site
	}
	if scope >= routing.ScopeAccount {
		p.Account = acct
	}
	if scope == routing.ScopeWorkspace {
		p.Workspace = work
	}
	return p
}

// compiled is the platform preset the rules make, failing the test when they are refused.
func compiled(tb testing.TB, rules string) routing.Preset {
	tb.Helper()
	p := layered(routing.ScopePlatform)
	p.Currency = usd
	if err := p.ReadRules([]byte(rules)); err != nil {
		tb.Fatalf("%s: %v", rules, err)
	}
	return p
}

// TestReadRulesRefuses: rules that widen what they inherit, values out of range, settings a layer may not write and
// shapes the code does not know are refused, so neither a write nor a snapshot ever holds them.
func TestReadRulesRefuses(t *testing.T) {
	t.Parallel()
	long := `{"default":{},"rules":[` + strings.Repeat(`{"author":"a","sort":"price"},`, 600) + `{"author":"b"}]}`
	var many []string
	for i := range 101 {
		many = append(many, fmt.Sprintf(`{"author":"a%d"}`, i))
	}
	for _, tc := range []struct {
		name, rules string
		scope       routing.PresetScope
		currency    bool
	}{
		{name: "an unknown member", rules: `{"default":{"colour":"red"}}`},
		{name: "an unknown rule kind", rules: `{"default":{},"rules":[{"provider":"x"}]}`},
		{name: "a default with an author", rules: `{"default":{"author":"a"}}`},
		{name: "an empty class list", rules: `{"default":{"classes":[]}}`},
		{name: "an empty policy list", rules: `{"default":{"policies":[]}}`},
		{name: "a star among classes", rules: `{"default":{"classes":["*","relay"]}}`},
		{name: "a repeated class", rules: `{"default":{"classes":["relay","relay"]}}`},
		{name: "a class that is no slug", rules: `{"default":{"classes":["Relay"]}}`},
		{name: "an unknown policy", rules: `{"default":{"policies":["forever"]}}`},
		{name: "an unknown sort", rules: `{"default":{"sort":"cheapest"}}`},
		{name: "a score above 100", rules: `{"default":{"min_score":101}}`},
		{name: "a confidence above 1", rules: `{"default":{"min_score_confidence":1.5}}`},
		{name: "a price as a number", rules: `{"default":{"max_price":{"prompt":3}}}`},
		{name: "a price too precise", rules: `{"default":{"max_price":{"prompt":"0.0000000001"}}}`},
		{name: "an image price", rules: `{"default":{"max_price":{"image":"1"}}}`},
		{name: "a rule widening the classes", rules: `{"default":{"classes":["official"]},"rules":[{"author":"a","classes":["official","relay"]}]}`},
		{name: "a rule widening to every class", rules: `{"default":{"classes":["official"]},"rules":[{"author":"a","classes":["*"]}]}`},
		{name: "a rule widening the policies", rules: `{"default":{"policies":["no_retention"]},"rules":[{"author":"a","policies":["*"]}]}`},
		{name: "a rule lowering the score", rules: `{"default":{"min_score":90},"rules":[{"author":"a","min_score":80}]}`},
		{name: "a rule turning verification off", rules: `{"default":{"verified_only":true},"rules":[{"author":"a","verified_only":false}]}`},
		{name: "a rule raising the price", rules: `{"default":{"max_price":{"prompt":"1"}},"rules":[{"author":"a","max_price":{"prompt":"2"}}]}`},
		{name: "a model rule widening its author's", rules: `{"default":{},"rules":[{"author":"a","classes":["official"]},{"model":"a/m","classes":["relay"]}]}`},
		{name: "a rule naming author and model", rules: `{"default":{},"rules":[{"author":"a","model":"a/m"}]}`},
		{name: "a rule naming neither", rules: `{"default":{},"rules":[{"sort":"price"}]}`},
		{name: "a model that is no slug", rules: `{"default":{},"rules":[{"model":"m"}]}`},
		{name: "a repeated author", rules: `{"default":{},"rules":[{"author":"a"},{"author":"a"}]}`},
		{name: "a repeated model", rules: `{"default":{},"rules":[{"model":"a/m"},{"model":"a/m"}]}`},
		{name: "too many rules", rules: `{"default":{},"rules":[` + strings.Join(many, ",") + `]}`},
		{name: "too long", rules: long},
		{name: "a closed order naming none", rules: `{"default":{},"advanced":{"no_fallbacks":true}}`},
		{name: "a repeated group", rules: `{"default":{},"advanced":{"order":["g1","g1"]}}`},
		{name: "too many attempts", rules: `{"default":{},"advanced":{"max_attempts":9}}`},
		{name: "a sticky time of years", rules: `{"default":{},"advanced":{"sticky_ttl":100000000}}`},
		{name: "a site preset with advanced settings", rules: `{"default":{},"advanced":{"max_attempts":2}}`, scope: routing.ScopeSite},
		{name: "a site preset naming a currency", rules: `{"default":{}}`, scope: routing.ScopeSite, currency: true},
		{name: "a priced platform preset without a currency", rules: `{"default":{"max_price":{"request":"0.01"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := layered(tc.scope)
			if tc.currency {
				p.Currency = usd
			}
			if err := p.ReadRules([]byte(tc.rules)); !errors.Is(err, routing.ErrRules) {
				t.Errorf("ReadRules: %v, want ErrRules", err)
			}
		})
	}
}

// TestReadRulesBoundsTheDurations: the platform's sticky time and attempt window are held to their bounds in whole
// seconds, so a number whose Duration would wrap negative is refused rather than taken as a window already over.
func TestReadRulesBoundsTheDurations(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		field string
		value int64
		ok    bool
	}{
		{"sticky_ttl", 86400, true},
		{"sticky_ttl", 86401, false},
		{"sticky_ttl", -1, false},
		{"sticky_ttl", math.MaxInt64, false},
		{"attempt_window", 600, true},
		{"attempt_window", 601, false},
		{"attempt_window", math.MaxInt64, false},
	} {
		p := layered(routing.ScopePlatform)
		err := p.ReadRules(fmt.Appendf(nil, `{"default":{},"advanced":{%q:%d}}`, tc.field, tc.value))
		if (err == nil) != tc.ok || err != nil && !errors.Is(err, routing.ErrRules) {
			t.Errorf("%s %d: %v", tc.field, tc.value, err)
		}
		if tc.ok && max(p.StickyTTL, p.AttemptWindow) != time.Duration(tc.value)*time.Second {
			t.Errorf("%s %d: read as %v and %v", tc.field, tc.value, p.StickyTTL, p.AttemptWindow)
		}
	}
}

// TestReadRulesInherits: a rule is whole once compiled. A filter it leaves out is its parent's, a model's parent is its
// author's rule, and a sort it sets replaces its parent's while one it leaves out is inherited.
func TestReadRulesInherits(t *testing.T) {
	t.Parallel()
	p := compiled(t, `{"default":{"sort":"price","classes":["official","cloud"],"max_price":{"prompt":"3"}},
		"rules":[{"model":"acme/m","max_price":{"completion":"5"}},{"author":"acme","sort":"latency","classes":["official"],"verified_only":true}],
		"advanced":{"order":["g2","g1"],"max_attempts":2,"sticky_ttl":60}}`)
	def := routing.Rule{
		Strategy: routing.Cheapest, Classes: routing.ClassSet{routing.ClassOfficial, routing.ClassCloud}, Policies: routing.AllPolicies,
		MaxPrice: limitOf(t, map[catalog.Item]string{catalog.ItemInput: "0.000003"}),
	}
	author := routing.Rule{
		Strategy: routing.LowestLatency, Classes: routing.ClassSet{routing.ClassOfficial}, Policies: routing.AllPolicies,
		VerifiedOnly: true, MaxPrice: def.MaxPrice,
	}
	model := author
	model.MaxPrice = limitOf(t, map[catalog.Item]string{catalog.ItemInput: "0.000003", catalog.ItemOutput: "0.000005"})
	for _, c := range []struct {
		what      string
		got, want any
	}{
		{"the default", p.Rule, def},
		{"the author's rule", p.Authors["acme"], author},
		{"the model's rule, over its author's", p.Models["acme/m"], model},
		{"the order", p.OrderNames, []string{"g2", "g1"}},
		{"the attempts and sticky time", [2]any{p.MaxAttempts, p.StickyTTL}, [2]any{2, time.Minute}},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.what, c.got, c.want)
		}
	}
}

// limitOf is a price limit of per-token prices.
func limitOf(tb testing.TB, prices map[catalog.Item]string) routing.PriceLimit {
	tb.Helper()
	var l routing.PriceLimit
	for it, p := range prices {
		l.Set.Add(it)
		l.Max[it] = price(tb, p)
	}
	return l
}

// presetHarness routes one model of the author acme between two endpoints that differ in everything a rule filters on:
// 1 is official, keeps no data, verified, scored and fast to answer; 2 is a relay that may train, unverified,
// unscored, of lower quality and half the price.
func presetHarness(t *testing.T, p routing.Preset) *harness {
	t.Helper()
	slug := func(e *routing.Endpoint) { e.ModelSlug = "acme/m" }
	one := endpoint(t, 1, slug, func(e *routing.Endpoint) {
		e.Score, e.ScoreConfidence, e.Latency = 100, 0.9, 100*time.Millisecond
	})
	two := endpoint(t, 2, slug, func(e *routing.Endpoint) {
		e.Class, e.Policy, e.Verified, e.Quality, e.Latency = routing.ClassRelay, routing.PolicyMayTrain, false, 0.5, 900*time.Millisecond
		e.Price.Schedule = schedule(t, "0.0000005", "0.000001", "")
	})
	return newHarness(t, routing.Config{}, p, one, two)
}

// presetInput is input() from the test's workspace on a site that keeps its accounts in dollars.
func presetInput() routing.Input {
	in := input()
	in.Admission.Workspace, in.Admission.Currency = work, usd
	return in
}

// route is the endpoints the plan tries, or nil when the plan leaves none and says the preset or the override did.
func (h *harness) route(in routing.Input) []routing.EndpointID {
	h.t.Helper()
	p, _, err := h.r.Plan(in)
	if none, ok := errors.AsType[*routing.NoEndpoint](err); ok {
		for _, e := range none.Reasons {
			if e.Reason != routing.PresetFilter && e.Reason != routing.OverrideFilter {
				h.t.Errorf("left out for %v", e.Reason)
			}
		}
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	var out []routing.EndpointID
	for _, e := range p.Entries {
		out = append(out, e.Endpoint)
	}
	return out
}

// TestPresetRulesRoute: each kind of rule changes the route as it says, on the default rule, on the model's author's
// rule and on the model's own; a rule for another author or model changes nothing. The model lists' view of the key
// keeps the endpoints the route may take, and the model while it keeps one.
func TestPresetRulesRoute(t *testing.T) {
	t.Parallel()
	cheapFirst, fastFirst := []routing.EndpointID{ep(2), ep(1)}, []routing.EndpointID{ep(1), ep(2)}
	for _, tc := range []struct {
		name, rules string
		want        []routing.EndpointID
	}{
		{"sort by price", `{"default":{"sort":"price"}}`, cheapFirst},
		{"classes", `{"default":{"sort":"price","classes":["official"]}}`, []routing.EndpointID{ep(1)}},
		{"data policies", `{"default":{"sort":"price","policies":["no_retention","retained"]}}`, []routing.EndpointID{ep(1)}},
		{"verified only", `{"default":{"sort":"price","verified_only":true}}`, []routing.EndpointID{ep(1)}},
		{"product score", `{"default":{"sort":"price","min_score":90}}`, []routing.EndpointID{ep(1)}},
		{"score confidence above every endpoint's", `{"default":{"min_score_confidence":0.95}}`, nil},
		{"a score of at least 0", `{"default":{"sort":"price","min_score":0}}`, []routing.EndpointID{ep(1)}},
		{"a confidence of at least 0", `{"default":{"sort":"price","min_score_confidence":0}}`, []routing.EndpointID{ep(1)}},
		{"a score of at least 0 the author's rule inherits", `{"default":{"min_score":0},"rules":[{"author":"acme","sort":"price"}]}`, []routing.EndpointID{ep(1)}},
		{"prompt price", `{"default":{"sort":"price","max_price":{"prompt":"0.6"}}}`, []routing.EndpointID{ep(2)}},
		{"completion price below both", `{"default":{"max_price":{"completion":"0.5"}}}`, nil},
		{"the author's classes", `{"default":{"sort":"price"},"rules":[{"author":"acme","classes":["relay"]}]}`, []routing.EndpointID{ep(2)}},
		{"another author's classes", `{"default":{"sort":"price"},"rules":[{"author":"zeta","classes":["relay"]}]}`, cheapFirst},
		{"the model's policies", `{"default":{"sort":"price"},"rules":[{"model":"acme/m","policies":["no_retention"]}]}`, []routing.EndpointID{ep(1)}},
		{"another model's policies", `{"default":{"sort":"price"},"rules":[{"model":"acme/n","policies":["no_retention"]}]}`, cheapFirst},
		{"the author's sort", `{"default":{"sort":"price"},"rules":[{"author":"acme","sort":"latency"}]}`, fastFirst},
		{"the model's sort over its author's", `{"default":{},"rules":[{"author":"acme","sort":"latency"},{"model":"acme/m","sort":"price"}]}`, cheapFirst},
		{"the platform's order", `{"default":{"sort":"price"},"advanced":{"order":["g1"]}}`, fastFirst},
		{"the platform's closed order", `{"default":{"sort":"price"},"advanced":{"order":["g1"],"no_fallbacks":true}}`, []routing.EndpointID{ep(1)}},
		{"the platform's quality floor", `{"default":{"sort":"price"},"advanced":{"min_quality":0.8}}`, []routing.EndpointID{ep(1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := presetHarness(t, compiled(t, tc.rules))
			if got := h.route(presetInput()); !slices.Equal(got, tc.want) {
				t.Errorf("route %v, want %v", got, tc.want)
			}
			checkView(t, h, tc.want)
		})
	}
}

// checkView checks that the key's view for the model lists keeps exactly the endpoints the route may take.
func checkView(t *testing.T, h *harness, route []routing.EndpointID) {
	t.Helper()
	v, err := h.r.KeyView(presetInput().Admission)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []routing.EndpointID{ep(1), ep(2)} {
		if v.Allows(model, e) != slices.Contains(route, e) {
			t.Errorf("the view allows %v: %v", e, v.Allows(model, e))
		}
	}
	if v.Serves(model) != (len(route) > 0) {
		t.Errorf("the view serves the model: %v", v.Serves(model))
	}
}

// TestRequestOnlyTightensThePreset: a request's provider object filters within what the key's preset allows. It
// cannot bring back a class, a policy or a price the preset left out; its sort reorders only what is left.
func TestRequestOnlyTightensThePreset(t *testing.T) {
	t.Parallel()
	sortPrice := routing.Cheapest
	limit := routing.PriceLimit{}
	limit.Set.Add(catalog.ItemInput)
	limit.Max[catalog.ItemInput] = price(t, "0.00001")
	for _, tc := range []struct {
		name, rules string
		over        routing.Override
		want        []routing.EndpointID
	}{
		{"another class", `{"default":{"classes":["official"]}}`, routing.Override{Classes: routing.ClassSet{routing.ClassRelay}}, nil},
		{"every policy", `{"default":{"policies":["no_retention"]}}`, routing.Override{Policies: routing.AllPolicies}, []routing.EndpointID{ep(1)}},
		{"a higher price", `{"default":{"max_price":{"prompt":"0.6"}}}`, routing.Override{MaxPrice: limit}, []routing.EndpointID{ep(2)}},
		{"an order outside the closed one", `{"default":{},"advanced":{"order":["g1"],"no_fallbacks":true}}`, routing.Override{OrderNames: []string{"g2"}}, []routing.EndpointID{ep(1)}},
		{"a sort over the model's", `{"default":{},"rules":[{"model":"acme/m","sort":"latency"}]}`, routing.Override{Sort: &sortPrice}, []routing.EndpointID{ep(2), ep(1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := presetInput()
			in.Override = tc.over
			if got := presetHarness(t, compiled(t, tc.rules)).route(in); !slices.Equal(got, tc.want) {
				t.Errorf("route %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPresetVisibility: a key is routed only by a preset of its own layers, and by a platform preset whose price limits
// are in its site's currency. Anything else is unavailable rather than routed by, whatever bound the key to it, and the
// model lists refuse it alike.
func TestPresetVisibility(t *testing.T) {
	t.Parallel()
	elsewhere := func(p *routing.Preset) {}
	for _, tc := range []struct {
		name   string
		scope  routing.PresetScope
		change func(*routing.Preset)
		ok     bool
	}{
		{"the platform's", routing.ScopePlatform, elsewhere, true},
		{"the platform's in the site's currency", routing.ScopePlatform, func(p *routing.Preset) { p.Currency = usd }, true},
		{"the platform's in another currency", routing.ScopePlatform, func(p *routing.Preset) { p.Currency = mustCurrency("CNY") }, false},
		{"its site's", routing.ScopeSite, elsewhere, true},
		{"another site's", routing.ScopeSite, func(p *routing.Preset) { p.Site = routing.SiteID(id(901)) }, false},
		{"its account's", routing.ScopeAccount, elsewhere, true},
		{"another account's", routing.ScopeAccount, func(p *routing.Preset) { p.Account = routing.AccountID(id(902)) }, false},
		{"its workspace's", routing.ScopeWorkspace, elsewhere, true},
		{"another workspace's", routing.ScopeWorkspace, func(p *routing.Preset) { p.Workspace = routing.WorkspaceID(id(903)) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := layered(tc.scope)
			p.Rule = routing.Rule{Classes: routing.AllClasses, Policies: routing.AllPolicies}
			tc.change(&p)
			h := newHarness(t, routing.Config{}, p, endpoint(t, 1))
			_, _, err := h.r.Plan(presetInput())
			if unavailable := errors.Is(err, routing.ErrPresetUnavailable); unavailable == tc.ok || !tc.ok && err == nil {
				t.Errorf("Plan: %v", err)
			}
			if _, err := h.r.KeyView(presetInput().Admission); errors.Is(err, routing.ErrPresetUnavailable) == tc.ok {
				t.Errorf("KeyView: %v", err)
			}
		})
	}
}

// BenchmarkPlanPreset is BenchmarkPlan's fifty candidates under the open preset and under one of a hundred rules, half by
// author and half by model, one of which is the planned model's: the rule lookup is one or two map reads per model, and
// allocates nothing, nor does counting the plan for the preset's alert (watched).
func BenchmarkPlanPreset(b *testing.B) {
	var rules []string
	for i := range 50 {
		rules = append(rules, fmt.Sprintf(`{"author":"a%d","sort":"price"}`, i), fmt.Sprintf(`{"model":"a%d/m","sort":"latency"}`, i))
	}
	ruled := compiled(b, `{"default":{"classes":["*"],"policies":["*"]},"rules":[`+strings.Join(rules, ",")+`]}`)
	ruled.ID = pre
	watched := routing.Config{Alerts: &signals{}, Node: "n1"} // a platform preset is then counted on every plan
	for _, tc := range []struct {
		name   string
		cfg    routing.Config
		preset routing.Preset
	}{{"open", routing.Config{}, openPreset()}, {"100 rules", routing.Config{}, ruled}, {"100 rules watched", watched, ruled}} {
		b.Run(tc.name, func(b *testing.B) {
			h, in := candidatesUnder(b, 50, tc.cfg, tc.preset)
			in.Admission.Currency = usd
			b.ReportAllocs()
			for b.Loop() {
				if _, wait, err := h.r.Plan(in); wait != nil || err != nil {
					b.Fatal(wait, err)
				}
			}
		})
	}
}
