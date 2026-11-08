package main

import (
	"net/http"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/routing"
)

// checkRouter is a router whose snapshot holds one platform endpoint of acme/m, official, may train, unverified, at 1
// per million prompt tokens and 2 per million completion tokens: what the fixtures check routing presets' rules against.
func checkRouter(t *testing.T) *routing.Router {
	t.Helper()
	r, err := routing.New(routing.Config{})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002"}}`))
	if err != nil {
		t.Fatal(err)
	}
	e := routing.Endpoint{
		ID: routing.EndpointID(uuid.NewV7()), Supplier: routing.SupplierID(uuid.NewV7()), Model: catalog.ModelID(uuid.NewV7()), ModelSlug: "acme/m",
		Class: routing.ClassOfficial, Policy: routing.PolicyMayTrain, Context: 128_000, ServiceTiers: 1 << catalog.ServiceTierStandard,
		Price: routing.PriceRef{Version: uuid.NewV7(), Schedule: prices},
	}
	snap, err := r.NewSnapshot(time.Now(), []routing.Endpoint{e}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Replace(snap)
	return r
}

// TestConsoleRoutingRulesCheck: the console checks rules as a write of the account's preset would, says what a list
// shows of them and names each rule that keeps none of the endpoints the account's keys can see, with the conditions
// that alone keep none; the presets it lists carry the same summary.
func TestConsoleRoutingRulesCheck(t *testing.T) {
	t.Parallel()
	f := newConsoleFixture(t)
	s := f.owner()
	classes, zero := []string{"cloud"}, 0
	for _, c := range []struct {
		what, rules string
		want        consoleapi.RoutingRulesCheck
	}{
		{"rules that keep an endpoint", `{"default":{"sort":"price","policies":["may_train"]}}`, consoleapi.RoutingRulesCheck{
			Summary: consoleapi.RoutingRulesSummary{Sort: consoleapi.Price}, Unmatched: []consoleapi.RoutingRuleUnmatched{},
		}},
		{"a class no endpoint has, inherited", `{"default":{"classes":["cloud"]},"rules":[{"model":"acme/m","max_price":{"prompt":"9"}}]}`, consoleapi.RoutingRulesCheck{
			Summary: consoleapi.RoutingRulesSummary{Sort: consoleapi.Balanced, Classes: &classes, Priced: true, Overrides: 1},
			Unmatched: []consoleapi.RoutingRuleUnmatched{
				{Fields: []consoleapi.RoutingRuleUnmatchedFields{consoleapi.Classes}},
				{Index: &zero, Fields: []consoleapi.RoutingRuleUnmatchedFields{}},
			},
		}},
	} {
		rec := f.do(http.MethodPost, "/routing-presets/checks", `{"rules":`+c.rules+`}`, as(s))
		expect(t, c.what, rec, http.StatusOK, "")
		var got consoleapi.RoutingRulesCheck
		decode(t, rec, &got)
		if diff := cmp.Diff(c.want, got); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.what, diff)
		}
	}
	expect(t, "the platform's settings", f.do(http.MethodPost, "/routing-presets/checks", `{"rules":{"default":{},"advanced":{}}}`, as(s)),
		http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest)
	f.routingPreset(s, `{"slug":"cloud","name":"Cloud","rules":{"default":{"classes":["cloud"],"verified_only":true}}}`)
	var list consoleapi.RoutingPresetList
	decode(t, f.do(http.MethodGet, "/routing-presets", "", as(s)), &list)
	want := &consoleapi.RoutingRulesSummary{Sort: consoleapi.Balanced, Classes: &classes, VerifiedOnly: true}
	if len(list.Data) != 1 || !cmp.Equal(list.Data[0].Summary, want) {
		t.Errorf("listed %+v, want one preset summarised as %+v", list.Data, want)
	}
}

// TestAdminRoutingRulesCheck: the admin checks rules as a platform preset's, with its currency, or as a site's, which
// names none, against the endpoints the layer's keys can see.
func TestAdminRoutingRulesCheck(t *testing.T) {
	t.Parallel()
	f := newAdminFixture(t)
	s := f.owner()
	rec := f.do(http.MethodPost, "/routing-presets/checks", `{"rules":{"default":{"max_price":{"prompt":"0.5","completion":"5"}}},"currency":"USD"}`, as(s))
	expectAdmin(t, "a platform preset", rec, http.StatusOK, "")
	var got adminapi.RoutingRulesCheck
	decode(t, rec, &got)
	if len(got.Unmatched) != 1 || got.Unmatched[0].Index != nil || !slices.Equal(got.Unmatched[0].Fields, []adminapi.RoutingRuleUnmatchedFields{adminapi.MaxPricePrompt}) || !got.Summary.Priced {
		t.Errorf("a platform preset: %+v", got)
	}
	site := `"site_id":"` + f.siteID() + `"`
	for _, c := range []struct {
		what, body string
		status     int
		code       errorCode
	}{
		{"a site's preset", `{"rules":{"default":{}},` + site + `}`, http.StatusOK, ""},
		{"a site's preset with a currency", `{"rules":{"default":{}},"currency":"USD",` + site + `}`, http.StatusBadRequest, codeInvalidRequest},
		{"a platform price without a currency", `{"rules":{"default":{"max_price":{"prompt":"1"}}}}`, http.StatusBadRequest, codeInvalidRequest},
		{"an unknown site", `{"rules":{"default":{}},"site_id":"site_01h455vb4pex5vsknk084sn02q"}`, http.StatusNotFound, codeNotFound},
	} {
		expectAdmin(t, c.what, f.do(http.MethodPost, "/routing-presets/checks", c.body, as(s)), c.status, c.code)
	}
}

// TestRoutingRulesWithNulls: rules that write a member as null, or the sort as "", which the server has always read as
// leaving it out, pass the contracts as before, so a page writes stored rules back unchanged.
func TestRoutingRulesWithNulls(t *testing.T) {
	t.Parallel()
	nulls := `{"default":{"sort":"","classes":null,"policies":null,"min_score":null,"min_score_confidence":null,"verified_only":null,` +
		`"max_price":{"prompt":null,"completion":"5","request":null}},"rules":[{"author":"acme","model":null,"max_price":null}],"advanced":null}`
	c := newConsoleFixture(t)
	s := c.owner()
	expect(t, "check", c.do(http.MethodPost, "/routing-presets/checks", `{"rules":`+nulls+`}`, as(s)), http.StatusOK, "")
	c.routingPreset(s, `{"slug":"nulls","name":"Nulls","rules":`+nulls+`}`)
	a := newAdminFixture(t)
	advanced := `{"default":{},"rules":null,"advanced":{"order":null,"no_fallbacks":null,"max_attempts":null,"sticky_ttl":null,` +
		`"attempt_window":null,"min_quality":null,"min_confidence":null}}`
	a.adminPreset(a.owner(), "/routing-presets", `{"slug":"nulls","name":"Nulls","rules":`+advanced+`,"reason":"stored before"}`)
}
