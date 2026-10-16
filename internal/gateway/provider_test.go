package gateway

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/routing"
)

// TestOverride: each member of the provider object either narrows the route or is refused by name; none is dropped.
func TestOverride(t *testing.T) {
	t.Parallel()
	price := func(s string) platform.UnitPrice {
		p, err := platform.ParseUnitPrice(s)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	cheapest, fastest := routing.Cheapest, routing.Fastest
	limit := func(item catalog.Item, p string) (l routing.PriceLimit) {
		l.Set.Add(item)
		l.Max[item] = price(p)
		return l
	}
	for name, tc := range map[string]struct {
		provider string
		want     routing.Override
	}{
		"none":                     {"", routing.Override{}},
		"null members":             {`{"sort":null,"only":null,"max_price":null}`, routing.Override{}},
		"sort by price":            {`{"sort":"price"}`, routing.Override{Sort: &cheapest}},
		"sort by throughput":       {`{"sort":"throughput"}`, routing.Override{Sort: &fastest}},
		"sort by latency":          {`{"sort":"latency"}`, routing.Override{Sort: &fastest}},
		"data collection denied":   {`{"data_collection":"deny"}`, routing.Override{Policies: noRetention}},
		"data collection allowed":  {`{"data_collection":"allow"}`, routing.Override{}},
		"zero data retention":      {`{"zdr":true}`, routing.Override{Policies: noRetention}},
		"no zero data retention":   {`{"zdr":false}`, routing.Override{}},
		"performance wishes":       {`{"preferred_min_throughput":50.5,"preferred_max_latency":1.5}`, routing.Override{MinThroughput: 50.5, MaxLatency: 1500 * time.Millisecond}},
		"the defaults, written":    {`{"order":[],"only":[],"ignore":[],"allow_fallbacks":true,"require_parameters":false}`, routing.Override{}},
		"a prompt price":           {`{"max_price":{"prompt":1.5}}`, routing.Override{MaxPrice: limit(catalog.ItemInput, "0.0000015")}},
		"a price with an exponent": {`{"max_price":{"completion":2e-1}}`, routing.Override{MaxPrice: limit(catalog.ItemOutput, "0.0000002")}},
		"a price of nothing":       {`{"max_price":{"prompt":0}}`, routing.Override{MaxPrice: limit(catalog.ItemInput, "0")}},
		"the smallest price":       {`{"max_price":{"prompt":0.000000001}}`, routing.Override{MaxPrice: limit(catalog.ItemInput, "0.000000000000001")}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var raw []byte
			if tc.provider != "" {
				raw = []byte(tc.provider)
			}
			got, err := override(raw)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, got, cmp.Comparer(func(a, b platform.UnitPrice) bool { return a.Cmp(b) == 0 })); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
	for name, tc := range map[string]struct{ provider, named string }{
		"suppliers to use":              {`{"only":["acme"]}`, "provider.only"},
		"suppliers to leave out":        {`{"ignore":["acme"]}`, "provider.ignore"},
		"an order of suppliers":         {`{"order":["acme"]}`, "provider.order"},
		"fallbacks off":                 {`{"allow_fallbacks":false}`, "provider.allow_fallbacks"},
		"every parameter required":      {`{"require_parameters":true}`, "provider.require_parameters"},
		"quantizations":                 {`{"quantizations":["fp8"]}`, "provider.quantizations"},
		"a member nobody knows":         {`{"colour":"red"}`, "provider.colour"},
		"a price of a request":          {`{"max_price":{"request":1}}`, "provider.max_price.request"},
		"a price of an image":           {`{"max_price":{"image":1}}`, "provider.max_price.image"},
		"a price in text":               {`{"max_price":{"prompt":"1"}}`, "provider.max_price.prompt"},
		"a negative price":              {`{"max_price":{"prompt":-1}}`, "provider.max_price.prompt"},
		"a price finer than the unit":   {`{"max_price":{"prompt":0.0000000001}}`, "provider.max_price.prompt"},
		"a price too dear to be one":    {`{"max_price":{"prompt":1e10}}`, "provider.max_price.prompt"},
		"a price with a huge exponent":  {`{"max_price":{"prompt":1e999999999}}`, "provider.max_price.prompt"},
		"prices that are not an object": {`{"max_price":1}`, "provider.max_price"},
		"sort by something else":        {`{"sort":"quality"}`, "provider.sort"},
		"sort as an object":             {`{"sort":{"by":"price"}}`, "provider.sort"},
		"percentile throughput":         {`{"preferred_min_throughput":{"p90":10}}`, "provider.preferred_min_throughput"},
		"negative latency":              {`{"preferred_max_latency":-1}`, "provider.preferred_max_latency"},
		"a data collection of maybe":    {`{"data_collection":"maybe"}`, "provider.data_collection"},
		"zdr as text":                   {`{"zdr":"yes"}`, "provider.zdr"},
		"a list that is not":            {`{"only":"acme"}`, "provider.only"},
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			t.Parallel()
			_, err := override([]byte(tc.provider))
			var ref *Refusal
			if !errors.As(err, &ref) || ref.Status != http.StatusBadRequest || !strings.Contains(ref.Message, tc.named) {
				t.Errorf("got %v, want a 400 naming %s", err, tc.named)
			}
		})
	}
}

// withProvider adds the provider object to the body of a request.
func withProvider(body, provider string) string {
	return strings.Replace(body, `{"model"`, `{"provider":`+provider+`,"model"`, 1)
}

// TestProviderNarrowsTheRoute: the provider object of a request reaches routing through the HTTP entry. The route is
// cut by the data policy and the price the request asks for, a limit nothing meets is a 404 that says the override
// did it, and a member that cannot be kept is a 400 that reserves nothing.
func TestProviderNarrowsTheRoute(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) {
		o.endpoints = func(name string, e *routing.Endpoint) {
			if name == "alpha-2" {
				e.Policy = routing.PolicyMayTrain
			}
		}
	})
	a := r.newAccount(1e9)
	routeOf := func(provider string) ([]routing.EndpointID, reply, string) {
		before := len(r.calls())
		resp, body := r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(withProvider(messageBody("acme/alpha"), provider)))
		var route []routing.EndpointID
		if calls := r.calls(); len(calls) > before {
			for _, e := range calls[len(calls)-1].Plan.Entries {
				route = append(route, e.Endpoint)
			}
		}
		return route, resp, body
	}
	both := []routing.EndpointID{r.endpoints["alpha-1"].ID, r.endpoints["alpha-2"].ID}
	only1 := []routing.EndpointID{r.endpoints["alpha-1"].ID}
	// alpha-1 charges 1 and 2 credits per million tokens, alpha-2 1 and 4, and alpha-2 may train on the data.
	for name, tc := range map[string]struct {
		provider string
		route    []routing.EndpointID
	}{
		"no provider object":      {`{}`, both},
		"data collection denied":  {`{"data_collection":"deny"}`, only1},
		"zero data retention":     {`{"zdr":true}`, only1},
		"a completion price cap":  {`{"max_price":{"completion":3}}`, only1},
		"a cap both meet":         {`{"max_price":{"prompt":1,"completion":4}}`, both},
		"a cap on the prompt":     {`{"max_price":{"prompt":0.5}}`, nil},
		"data collection allowed": {`{"data_collection":"allow"}`, both},
	} {
		route, resp, body := routeOf(tc.provider)
		switch {
		case tc.route == nil:
			if code, _ := errorOf(t, body); resp.StatusCode != http.StatusNotFound || code != "no_endpoint" || !strings.Contains(body, "override_filter") {
				t.Errorf("%s: %d %s, want a 404 no_endpoint by the override", name, resp.StatusCode, body)
			}
		case resp.StatusCode != http.StatusOK || !cmp.Equal(route, tc.route):
			t.Errorf("%s: %d, route %v, want %v: %s", name, resp.StatusCode, route, tc.route, body)
		}
	}
	for _, provider := range []string{`{"only":["acme"]}`, `{"allow_fallbacks":false}`, `{"max_price":{"request":1}}`} {
		before := len(r.calls())
		_, resp, body := routeOf(provider)
		if code, _ := errorOf(t, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_request" || len(r.calls()) != before {
			t.Errorf("%s: %d %s", provider, resp.StatusCode, body)
		}
	}
	if r.count(pendingCount) != 0 {
		t.Errorf("%d reservations are pending", r.count(pendingCount))
	}
	r.idle(1 << 30)
}
