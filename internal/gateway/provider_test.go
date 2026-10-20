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
	"axiom/internal/platform/alloctest"
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
		"a price in units of ten":  {`{"max_price":{"prompt":1e1}}`, routing.Override{MaxPrice: limit(catalog.ItemInput, "0.00001")}},
		"the dearest price":        {`{"max_price":{"prompt":1E+9}}`, routing.Override{MaxPrice: limit(catalog.ItemInput, "1000")}},
		"a long fraction of zeros": {`{"max_price":{"prompt":1.500000000e0}}`, routing.Override{MaxPrice: limit(catalog.ItemInput, "0.0000015")}},
		"zero with an exponent":    {`{"max_price":{"prompt":0e999}}`, routing.Override{MaxPrice: limit(catalog.ItemInput, "0")}},
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
}

// TestOverrideRefuses: a member that cannot be kept, or is not valid, is a 400 that names it.
func TestOverrideRefuses(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ provider, named string }{
		"suppliers to use":                              {`{"only":["acme"]}`, "provider.only"},
		"suppliers to leave out":                        {`{"ignore":["acme"]}`, "provider.ignore"},
		"an order of suppliers":                         {`{"order":["acme"]}`, "provider.order"},
		"fallbacks off":                                 {`{"allow_fallbacks":false}`, "provider.allow_fallbacks"},
		"every parameter required":                      {`{"require_parameters":true}`, "provider.require_parameters"},
		"quantizations":                                 {`{"quantizations":["fp8"]}`, "provider.quantizations"},
		"a member nobody knows":                         {`{"colour":"red"}`, "provider.colour"},
		"a price of a request":                          {`{"max_price":{"request":1}}`, "provider.max_price.request"},
		"a price of an image":                           {`{"max_price":{"image":1}}`, "provider.max_price.image"},
		"a price in text":                               {`{"max_price":{"prompt":"1"}}`, "provider.max_price.prompt"},
		"a negative price":                              {`{"max_price":{"prompt":-1}}`, "provider.max_price.prompt"},
		"a price finer than the unit":                   {`{"max_price":{"prompt":0.0000000001}}`, "provider.max_price.prompt"},
		"a price too dear to be one":                    {`{"max_price":{"prompt":1e10}}`, "provider.max_price.prompt"},
		"prices that are not an object":                 {`{"max_price":1}`, "provider.max_price"},
		"sort by something else":                        {`{"sort":"quality"}`, "provider.sort"},
		"sort by latency":                               {`{"sort":"latency"}`, "provider.sort latency"},
		"a price with an exponent of a million":         {`{"max_price":{"prompt":1e1000000}}`, "provider.max_price.prompt"},
		"a price with a negative exponent of a million": {`{"max_price":{"prompt":1e-1000000}}`, "provider.max_price.prompt"},
		"a price with a four-digit exponent":            {`{"max_price":{"prompt":1e1000}}`, "provider.max_price.prompt"},
		"a price with digits past what a price takes":   {`{"max_price":{"prompt":12345678901234567890}}`, "provider.max_price.prompt"},
		"a price of many zeros":                         {`{"max_price":{"prompt":0.00000000000000000000000000000001}}`, "provider.max_price.prompt"},
		"sort as an object":                             {`{"sort":{"by":"price"}}`, "provider.sort"},
		"percentile throughput":                         {`{"preferred_min_throughput":{"p90":10}}`, "provider.preferred_min_throughput"},
		"negative latency":                              {`{"preferred_max_latency":-1}`, "provider.preferred_max_latency"},
		"a data collection of maybe":                    {`{"data_collection":"maybe"}`, "provider.data_collection"},
		"zdr as text":                                   {`{"zdr":"yes"}`, "provider.zdr"},
		"a list that is not":                            {`{"only":"acme"}`, "provider.only"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := override([]byte(tc.provider))
			var ref *Refusal
			if !errors.As(err, &ref) || ref.Status != http.StatusBadRequest || !strings.Contains(ref.Message, tc.named) {
				t.Errorf("got %v, want a 400 naming %s", err, tc.named)
			}
		})
	}
}

// sendWith posts a request for acme/alpha with the provider object and returns the route it was admitted with, if it
// was.
func (r *rig) sendWith(a account, provider string) (route []routing.EndpointID, resp reply, body string) {
	r.t.Helper()
	return r.sendNamed(a, "acme/alpha", `"provider":`+provider+`,`)
}

// sendNamed posts a request for the model, written as given, with members (JSON members, each ending in a comma)
// before it, and returns the route it was admitted with, if it was.
func (r *rig) sendNamed(a account, model, members string) (route []routing.EndpointID, resp reply, body string) {
	r.t.Helper()
	before := len(r.calls())
	req := strings.Replace(messageBody(model), `{"model"`, `{`+members+`"model"`, 1)
	resp, body = r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(req))
	if calls := r.calls(); len(calls) > before {
		for _, e := range calls[len(calls)-1].Plan.Entries {
			route = append(route, e.Endpoint)
		}
	}
	return route, resp, body
}

// TestProviderNarrowsTheRoute: the provider object of a request reaches routing through the HTTP entry. The route is
// cut by the data policy and the price the request asks for, and a limit nothing meets is a 404 that says the override
// did it.
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
		"data collection allowed": {`{"data_collection":"allow"}`, both},
	} {
		if route, resp, body := r.sendWith(a, tc.provider); resp.StatusCode != http.StatusOK || !cmp.Equal(route, tc.route) {
			t.Errorf("%s: %d, route %v, want %v: %s", name, resp.StatusCode, route, tc.route, body)
		}
	}
	_, resp, body := r.sendWith(a, `{"max_price":{"prompt":0.5}}`)
	if code, _ := errorOf(t, body); resp.StatusCode != http.StatusNotFound || code != "no_endpoint" || !strings.Contains(body, "override_filter") {
		t.Errorf("a cap nothing meets: %d %s, want a 404 no_endpoint by the override", resp.StatusCode, body)
	}
	r.idle(1 << 30)
}

// TestProviderRefusedMembersReserveNothing: a member that cannot be kept is a 400 before anything is reserved.
func TestProviderRefusedMembersReserveNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	for _, provider := range []string{`{"only":["acme"]}`, `{"allow_fallbacks":false}`, `{"max_price":{"request":1}}`} {
		_, resp, body := r.sendWith(a, provider)
		if code, _ := errorOf(t, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_request" {
			t.Errorf("%s: %d %s", provider, resp.StatusCode, body)
		}
	}
	if r.count(allRows) != 0 || len(r.calls()) != 0 {
		t.Errorf("%d reservation rows, %d calls", r.count(allRows), len(r.calls()))
	}
	r.idle(1 << 30)
}

// TestPriceParsingIsBounded: what an exponent asks for is never computed. A price whose exponent or number of digits
// would make a huge integer is refused by the bound check, which is string work; the big integer is made only from
// what that check accepted. The bytes a refused price allocates are measured in a child process, where no other
// goroutine of this test binary adds to the process's count: a million-digit integer would be hundreds of kilobytes.
func TestPriceParsingIsBounded(t *testing.T) {
	huge := []string{"1e1000000", "1e-1000000", "1e999", "1e-999", "0.1e999999999"}
	t.Run("bounds", func(t *testing.T) { checkPriceBounds(t, huge) })
	t.Run("allocations", func(t *testing.T) { checkPriceAllocations(t, huge) })
}

// checkPriceBounds confirms that the huge exponents are refused by the bound check before any big integer is made.
func checkPriceBounds(t *testing.T, huge []string) {
	t.Helper()
	for _, number := range huge {
		if _, ok := readPrice([]byte(number)); ok {
			t.Errorf("%s passed the bound check", number)
		}
		if _, ok := perToken([]byte(number)); ok {
			t.Errorf("%s was accepted", number)
		}
	}
	for _, number := range []string{"1e3", "0e999", "0", "0.0000015"} {
		if _, ok := readPrice([]byte(number)); !ok {
			t.Errorf("%s was refused by the bound check", number)
		}
	}
}

// checkPriceAllocations holds each refused price to the bytes a short parse allocates.
func checkPriceAllocations(t *testing.T, huge []string) {
	t.Helper()
	var funcs []alloctest.Function
	for _, number := range huge {
		funcs = append(funcs, alloctest.Function{Name: number, F: func() { perToken([]byte(number)) }})
	}
	got := alloctest.Measure(t, funcs...)
	for _, number := range huge {
		if got[number].Bytes > 4<<10 {
			t.Errorf("%s: %v bytes allocated, want a few hundred", number, got[number].Bytes)
		}
	}
}

// TestSortRanksAsAsked: sort throughput ranks by throughput, price by price, and latency is refused, not served as
// throughput. The endpoints are made so that every ranking differs: alpha-2 is the faster but dearer, alpha-1 the
// cheaper with the better first token.
func TestSortRanksAsAsked(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) {
		o.endpoints = func(name string, e *routing.Endpoint) {
			if name == "alpha-1" {
				e.Throughput, e.Latency = 50, 100*time.Millisecond
			}
			if name == "alpha-2" {
				e.Throughput, e.Latency = 100, 5*time.Second
			}
		}
	})
	a := r.newAccount(1e9)
	one, two := r.endpoints["alpha-1"].ID, r.endpoints["alpha-2"].ID
	for provider, want := range map[string][]routing.EndpointID{
		`{"sort":"price"}`:      {one, two},
		`{"sort":"throughput"}`: {two, one},
	} {
		if route, resp, body := r.sendWith(a, provider); resp.StatusCode != http.StatusOK || !cmp.Equal(route, want) {
			t.Errorf("%s: %d, route %v, want %v: %s", provider, resp.StatusCode, route, want, body)
		}
	}
	before := len(r.calls())
	_, resp, body := r.sendWith(a, `{"sort":"latency"}`)
	if code, _ := errorOf(t, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_request" || !strings.Contains(body, "provider.sort latency") || len(r.calls()) != before {
		t.Errorf("sort latency: %d %s", resp.StatusCode, body)
	}
	r.idle(1 << 30)
}
