package gateway

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/alloctest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
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
	cheapest, fastest, latency := routing.Cheapest, routing.Fastest, routing.LowestLatency
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
		"sort by latency":          {`{"sort":"latency"}`, routing.Override{Sort: &latency}},
		"sort object default":      {`{"sort":{"by":"price"}}`, routing.Override{Sort: &cheapest}},
		"null sort partition":      {`{"sort":{"by":"price","partition":null}}`, routing.Override{Sort: &cheapest}},
		"sort by model":            {`{"sort":{"by":"throughput","partition":"model"}}`, routing.Override{Sort: &fastest}},
		"sort across models":       {`{"sort":{"by":"latency","partition":"none"}}`, routing.Override{Sort: &latency, SortGlobal: true}},
		"supplier selection":       {`{"order":["g2","g1"],"only":["g1"],"ignore":["g3"]}`, routing.Override{OrderNames: []string{"g2", "g1"}, OnlyNames: []string{"g1"}, ExcludeNames: []string{"g3"}}},
		"fallbacks disabled":       {`{"allow_fallbacks":false}`, routing.Override{NoFallbacks: true}},
		"every parameter required": {`{"require_parameters":true}`, routing.Override{RequireParameters: true}},
		"empty quantizations":      {`{"quantizations":[]}`, routing.Override{}},
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
		"a price with an exponent of a million":         {`{"max_price":{"prompt":1e1000000}}`, "provider.max_price.prompt"},
		"a price with a negative exponent of a million": {`{"max_price":{"prompt":1e-1000000}}`, "provider.max_price.prompt"},
		"a price with a four-digit exponent":            {`{"max_price":{"prompt":1e1000}}`, "provider.max_price.prompt"},
		"a price with digits past what a price takes":   {`{"max_price":{"prompt":12345678901234567890}}`, "provider.max_price.prompt"},
		"a price of many zeros":                         {`{"max_price":{"prompt":0.00000000000000000000000000000001}}`, "provider.max_price.prompt"},
		"sort partition invalid":                        {`{"sort":{"by":"price","partition":"account"}}`, "provider.sort.partition"},
		"sort object missing by":                        {`{"sort":{"partition":"none"}}`, "provider.sort"},
		"sort unknown member":                           {`{"sort":{"by":"price","percentile":"p90"}}`, "provider.sort.percentile"},
		"empty provider name":                           {`{"only":[""]}`, "provider.only"},
		"nonstring provider name":                       {`{"ignore":[1]}`, "provider.ignore"},
		"fallbacks as text":                             {`{"allow_fallbacks":"false"}`, "provider.allow_fallbacks"},
		"parameters as text":                            {`{"require_parameters":"true"}`, "provider.require_parameters"},
		"percentile latency":                            {`{"preferred_max_latency":{"p50":1,"p99":2}}`, "provider.preferred_max_latency"},
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

// TestProviderRefusalsNameTheMissingData: a constraint the snapshot cannot keep says what it lacks, not just which
// member was refused: quantization levels, percentile windows and per-request/per-image price items are absent data,
// not values to guess.
func TestProviderRefusalsNameTheMissingData(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ provider, want string }{
		"quantizations":         {`{"quantizations":["fp8"]}`, "no quantization level"},
		"throughput percentile": {`{"preferred_min_throughput": { "p90": 10 }}`, "EWMA"},
		"latency percentile":    {`{"preferred_max_latency":{"p99":1}}`, "EWMA"},
		"sort percentile":       {`{"sort":{"by":"price","percentile":"p90"}}`, "no percentile measures"},
		"request price":         {`{"max_price":{"request":1}}`, "no per-request or per-image item"},
		"image price":           {`{"max_price":{"image":1}}`, "no per-request or per-image item"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := override([]byte(tc.provider))
			var ref *Refusal
			if !errors.As(err, &ref) || !strings.Contains(ref.Message, tc.want) {
				t.Errorf("got %v, want a refusal saying %q", err, tc.want)
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
		"no provider object":            {`{}`, both},
		"data collection denied":        {`{"data_collection":"deny"}`, only1},
		"zero data retention":           {`{"zdr":true}`, only1},
		"a completion price cap":        {`{"max_price":{"completion":3}}`, only1},
		"a cap both meet":               {`{"max_price":{"prompt":1,"completion":4}}`, both},
		"only a named group":            {`{"only":["alpha-1"]}`, only1},
		"ignore a named group":          {`{"ignore":["alpha-2"]}`, only1},
		"named order":                   {`{"order":["alpha-2","alpha-1"]}`, []routing.EndpointID{both[1], both[0]}},
		"order allows others":           {`{"order":["alpha-2"]}`, []routing.EndpointID{both[1], both[0]}},
		"closed two-group order":        {`{"order":["alpha-2","alpha-1"],"allow_fallbacks":false}`, []routing.EndpointID{both[1], both[0]}},
		"closed order":                  {`{"order":["alpha-1"],"allow_fallbacks":false}`, only1},
		"no fallback or order":          {`{"allow_fallbacks":false}`, only1},
		"empty order disables fallback": {`{"order":[],"allow_fallbacks":false}`, only1},
		"unknown ignore":                {`{"ignore":["missing"]}`, both},
		"unknown preferred group":       {`{"order":["missing"]}`, both},
		"data collection allowed":       {`{"data_collection":"allow"}`, both},
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

// TestProviderRequiresEveryParameter: require_parameters keeps the endpoints that declare every parameter the
// request sets; one the catalog does not declare at all is a 400, and one no endpoint declares is the same 404 as any
// other override that leaves nothing.
func TestProviderRequiresEveryParameter(t *testing.T) {
	t.Parallel()
	var all, noTemperature protocol.ParameterSet
	for p := protocol.Parameter(0); p < protocol.NumParameters; p++ {
		all.Add(p)
	}
	noTemperature = all &^ (protocol.ParameterSet(1) << protocol.ParamTemperature)
	r := newRig(t, func(o *rigOptions) {
		o.endpoints = func(name string, e *routing.Endpoint) {
			if name == "alpha-2" {
				e.Parameters = noTemperature
			} else {
				e.Parameters = all
			}
		}
	})
	a := r.newAccount(1e9)
	only1 := []routing.EndpointID{r.endpoints["alpha-1"].ID}
	both := []routing.EndpointID{r.endpoints["alpha-1"].ID, r.endpoints["alpha-2"].ID}
	for name, tc := range map[string]struct {
		members string
		want    []routing.EndpointID
	}{
		"without the guarantee": {`"temperature":0.5,`, both},
		"with the guarantee":    {`"temperature":0.5,"provider":{"require_parameters":true},`, only1},
		"no parameter set":      {`"provider":{"require_parameters":true},`, both},
	} {
		t.Run(name, func(t *testing.T) {
			route, resp, body := r.sendNamed(a, "acme/alpha", tc.members)
			if resp.StatusCode != http.StatusOK || !cmp.Equal(route, tc.want) {
				t.Errorf("%d, route %v, want %v: %s", resp.StatusCode, route, tc.want, body)
			}
		})
	}
	if _, resp, body := r.sendNamed(a, "acme/alpha", `"seed":7,"provider":{"require_parameters":true},`); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "provider.require_parameters") || !strings.Contains(body, "no endpoint parameter inventory") {
		t.Errorf("seed with the guarantee: %d %s", resp.StatusCode, body)
	}
	// The same snapshot with no endpoint declaring temperature: the guarantee leaves nothing, as any override does.
	endpoints := append([]routing.Endpoint(nil), r.routes...)
	for i := range endpoints {
		if endpoints[i].Model == r.alpha {
			endpoints[i].Parameters = noTemperature
		}
	}
	r.install(routing.Preset{Classes: routing.AllClasses, Policies: routing.AllPolicies, Strategy: routing.Cheapest}, endpoints...)
	if _, resp, body := r.sendNamed(a, "acme/alpha", `"temperature":0.5,"provider":{"require_parameters":true},`); resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "override_filter") {
		t.Errorf("no endpoint with temperature: %d %s", resp.StatusCode, body)
	}
	r.idle(1 << 30)
}

// TestProviderRefusedMembersReserveNothing: a member that cannot be kept is a 400 before anything is reserved.
func TestProviderRefusedMembersReserveNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	for _, provider := range []string{`{"quantizations":["fp8"]}`, `{"preferred_min_throughput":{"p90":10}}`, `{"max_price":{"request":1}}`, `{"max_price":{"image":1}}`} {
		_, resp, body := r.sendWith(a, provider)
		if code, _ := errorOf(t, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_request" {
			t.Errorf("%s: %d %s", provider, resp.StatusCode, body)
		}
	}
	// require_parameters with a parameter the catalog does not declare is refused before anything is reserved too.
	if _, resp, body := r.sendNamed(a, "acme/alpha", `"seed":7,"provider":{"require_parameters":true},`); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "provider.require_parameters") {
		t.Errorf("require_parameters with seed: %d %s", resp.StatusCode, body)
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

// TestSortRanksAsAsked: each strategy uses its own metric. The endpoints are made so that every ranking differs: alpha-2 is the faster but dearer, alpha-1 the
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
		`{"sort":"price"}`:                              {one, two},
		`{"sort":"throughput"}`:                         {two, one},
		`{"sort":"latency"}`:                            {one, two},
		`{"sort":{"by":"latency","partition":"model"}}`: {one, two},
	} {
		if route, resp, body := r.sendWith(a, provider); resp.StatusCode != http.StatusOK || !cmp.Equal(route, want) {
			t.Errorf("%s: %d, route %v, want %v: %s", provider, resp.StatusCode, route, want, body)
		}
	}
	beta := r.endpoints["beta-1"].ID
	for _, tc := range []struct {
		partition string
		closed    bool
		want      []routing.EndpointID
	}{
		{"model", false, []routing.EndpointID{one, two, beta}},
		{"none", false, []routing.EndpointID{one, beta, two}},
		{"model", true, []routing.EndpointID{one, beta}},
		{"none", true, []routing.EndpointID{one, beta}},
	} {
		members := `"models":["acme/beta"],"provider":{"sort":{"by":"latency","partition":"` + tc.partition + `"},"allow_fallbacks":` + strconv.FormatBool(!tc.closed) + `},`
		if route, resp, body := r.sendNamed(a, "acme/alpha", members); resp.StatusCode != http.StatusOK || !cmp.Equal(route, tc.want) {
			t.Errorf("partition %s: %d, route %v, want %v: %s", tc.partition, resp.StatusCode, route, tc.want, body)
		}
	}
	r.idle(1 << 30)
}

func TestProviderErrorsKeepStringCodes(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	for _, tc := range []struct{ provider, members, member string }{
		{`{"require_parameters":true}`, `"seed":7,`, "require_parameters"},
		{`{"quantizations":["fp8"]}`, "", "quantizations"},
		{`{"preferred_min_throughput":{"p90":10}}`, "", "preferred_min_throughput"},
		{`{"preferred_max_latency":{"p99":1}}`, "", "preferred_max_latency"},
		{`{"max_price":{"request":0}}`, "", "max_price.request"},
		{`{"max_price":{"image":0.01}}`, "", "max_price.image"},
		{`{"sort":{"by":"price","percentile":"p90"}}`, "", "sort.percentile"},
	} {
		body := `{"model":"acme/alpha",` + tc.members + `"provider":` + tc.provider + `,"messages":[{"role":"user","content":"hello"}]}`
		resp, out := r.do(http.MethodPost, "/api/v1/chat/completions", a.key, strings.NewReader(body))
		var got struct {
			Error struct {
				Code     string `json:"code"`
				Message  string `json:"message"`
				Metadata struct {
					Type string `json:"error_type"`
				} `json:"metadata"`
			} `json:"error"`
		}
		if err := jsonx.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusBadRequest || got.Error.Code != "invalid_request" || got.Error.Metadata.Type != "invalid_request" || !strings.Contains(got.Error.Message, "provider."+tc.member) {
			t.Errorf("%s: %d %s", tc.provider, resp.StatusCode, out)
		}
	}
	if r.count(allRows) != 0 || len(r.calls()) != 0 {
		t.Fatal("unsupported provider constraint reached reservation or upstream")
	}
	r.idle(1 << 30)
}
