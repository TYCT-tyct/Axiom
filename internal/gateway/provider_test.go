package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
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
		"null quantizations":       {`{"quantizations":null}`, routing.Override{}},
		"quantization levels":      {`{"quantizations":["fp8","unknown","fp8"]}`, routing.Override{Quantizations: []suppliers.Quantization{suppliers.QuantizationFP8, suppliers.QuantizationUnknown}}},
		"data collection denied":   {`{"data_collection":"deny"}`, routing.Override{Policies: noRetention}},
		"data collection allowed":  {`{"data_collection":"allow"}`, routing.Override{}},
		"zero data retention":      {`{"zdr":true}`, routing.Override{Policies: noRetention}},
		"no zero data retention":   {`{"zdr":false}`, routing.Override{}},
		"performance wishes":       {`{"preferred_min_throughput":50.5,"preferred_max_latency":1.5}`, routing.Override{MinThroughput: 50.5, MaxLatency: 1500 * time.Millisecond}},
		"throughput percentiles":   {`{"preferred_min_throughput":{"p50":100,"p99":10}}`, routing.Override{MinThroughputPercentiles: routing.Cutoffs{Values: [4]float64{100, 0, 0, 10}, Set: 1<<routing.P50 | 1<<routing.P99}}},
		"latency percentiles":      {`{"preferred_max_latency":{"p75":2,"p90":1.5}}`, routing.Override{MaxLatencyPercentiles: routing.Cutoffs{Values: [4]float64{0, 2, 1.5, 0}, Set: 1<<routing.P75 | 1<<routing.P90}}},
		"the defaults, written":    {`{"order":[],"only":[],"ignore":[],"allow_fallbacks":true,"require_parameters":false}`, routing.Override{}},
		"a prompt price":           {`{"max_price":{"prompt":1.5}}`, routing.Override{MaxPrice: limit(catalog.ItemInput, "0.0000015")}},
		"a price with an exponent": {`{"max_price":{"completion":2e-1}}`, routing.Override{MaxPrice: limit(catalog.ItemOutput, "0.0000002")}},
		"a price of nothing":       {`{"max_price":{"prompt":0}}`, routing.Override{MaxPrice: limit(catalog.ItemInput, "0")}},
		"the smallest price":       {`{"max_price":{"prompt":0.000000001}}`, routing.Override{MaxPrice: limit(catalog.ItemInput, "0.000000000000001")}},
		"a price in units of ten":  {`{"max_price":{"prompt":1e1}}`, routing.Override{MaxPrice: limit(catalog.ItemInput, "0.00001")}},
		"the dearest price":        {`{"max_price":{"prompt":1E+9}}`, routing.Override{MaxPrice: limit(catalog.ItemInput, "1000")}},
		"a long fraction of zeros": {`{"max_price":{"prompt":1.500000000e0}}`, routing.Override{MaxPrice: limit(catalog.ItemInput, "0.0000015")}},
		"zero with an exponent":    {`{"max_price":{"prompt":0e999}}`, routing.Override{MaxPrice: limit(catalog.ItemInput, "0")}},
		"a request price":          {`{"max_price":{"request":1.5}}`, routing.Override{MaxPrice: limit(catalog.ItemRequest, "1.5")}},
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
		"a quantization level nobody knows":             {`{"quantizations":["fp8","int3"]}`, "provider.quantizations"},
		"a quantization that is not a name":             {`{"quantizations":[8]}`, "provider.quantizations"},
		"a member nobody knows":                         {`{"colour":"red"}`, "provider.colour"},
		"a price of an image":                           {`{"max_price":{"image":1}}`, "provider.max_price.image"},
		"a negative request price":                      {`{"max_price":{"request":-1}}`, "provider.max_price.request"},
		"a request price finer than the unit":           {`{"max_price":{"request":0.0000000000000001}}`, "provider.max_price.request"},
		"a request price too dear to be one":            {`{"max_price":{"request":1000.1}}`, "provider.max_price.request"},
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
		"a percentile nobody knows":                     {`{"preferred_max_latency":{"p95":1}}`, "provider.preferred_max_latency.p95"},
		"a percentile that is not a number":             {`{"preferred_min_throughput":{"p50":"10"}}`, "provider.preferred_min_throughput.p50"},
		"an empty percentile object":                    {`{"preferred_max_latency":{}}`, "provider.preferred_max_latency"},
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

// quantizationLevels are the values OpenRouter documents (OpenRouter《Provider Routing》「Quantization」, 2026-10-31).
var quantizationLevels = []string{
	"int4", "int8", "fp4", "mxfp4", "nvfp4", "fp6", "fp8", "mxfp8", "fp16", "bf16", "fp32", "unknown",
}

// TestProviderRefusalsNameTheMissingData: a constraint the snapshot cannot keep says what it lacks, not just which
// member was refused. Quantization, the percentile windows and the per-request price are served now; the per-image
// price and the price sort's percentile are still absent data.
func TestProviderRefusalsNameTheMissingData(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ provider, want string }{
		"sort percentile":  {`{"sort":{"by":"price","percentile":"p90"}}`, "a sort object takes by and partition"},
		"image price":      {`{"max_price":{"image":1}}`, "item to compare is undecided"},
		"zero image price": {`{"max_price":{"image":0}}`, "item to compare is undecided"},
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

// TestQuantizationLevels: every level OpenRouter documents is a name this Axiom knows, so a request naming one is kept
// for routing to match.
func TestQuantizationLevels(t *testing.T) {
	t.Parallel()
	for _, level := range quantizationLevels {
		o, err := override([]byte(fmt.Sprintf(`{"quantizations":[%q]}`, level)))
		if err != nil || !slices.Equal(o.Quantizations, []suppliers.Quantization{suppliers.Quantization(level)}) {
			t.Errorf("%s: %v %v", level, o.Quantizations, err)
		}
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

// allParameters is every parameter the catalog can declare.
func allParameters() (all protocol.ParameterSet) {
	for p := range protocol.NumParameters {
		all = all.With(p)
	}
	return all
}

// TestProviderRequiresEveryParameter: require_parameters keeps the endpoints that declare every parameter the
// request sets; one the catalog does not declare at all is a 400.
func TestProviderRequiresEveryParameter(t *testing.T) {
	t.Parallel()
	all := allParameters()
	noTemperature := all &^ (protocol.ParameterSet(1) << protocol.ParamTemperature)
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
	// An empty member name is undeclared, not the end of the scan: it must refuse before the temperature behind it can
	// be ignored.
	if _, resp, body := r.sendNamed(a, "acme/alpha", `"":1,"temperature":0.5,"provider":{"require_parameters":true},`); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "provider.require_parameters") {
		t.Errorf("empty name before temperature: %d %s", resp.StatusCode, body)
	}
	r.idle(1 << 30)
}

// TestProviderRequiresEveryParameterNoEndpoint: with no endpoint declaring temperature the guarantee leaves nothing,
// as any override does.
func TestProviderRequiresEveryParameterNoEndpoint(t *testing.T) {
	t.Parallel()
	noTemperature := allParameters() &^ (protocol.ParameterSet(1) << protocol.ParamTemperature)
	r := newRig(t, func(o *rigOptions) {
		o.endpoints = func(_ string, e *routing.Endpoint) {
			e.Parameters = noTemperature
		}
	})
	a := r.newAccount(1e9)
	if _, resp, body := r.sendNamed(a, "acme/alpha", `"temperature":0.5,"provider":{"require_parameters":true},`); resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "override_filter") {
		t.Errorf("no endpoint with temperature: %d %s", resp.StatusCode, body)
	}
	r.idle(1 << 30)
}

// TestProviderServesQuantizationAndPerformanceWindows: the members that used to be refused now narrow the route by
// the level an admin recorded and by the rolling-window percentiles, and a level nobody recorded matches nothing.
func TestProviderServesQuantizationAndPerformanceWindows(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) {
		o.endpoints = func(name string, e *routing.Endpoint) {
			switch name {
			case "alpha-1":
				e.Quantization = suppliers.QuantizationFP8
				e.ThroughputPercentiles = [4]float64{500, 500, 500, 500}
				e.LatencyPercentiles = [4]time.Duration{time.Second, time.Second, time.Second, 5 * time.Second}
			case "alpha-2":
				e.Quantization = suppliers.QuantizationFP16
				e.ThroughputPercentiles = [4]float64{10, 10, 10, 10}
				e.LatencyPercentiles = [4]time.Duration{100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond}
			}
		}
	})
	a := r.newAccount(1e9)
	one, two := r.endpoints["alpha-1"].ID, r.endpoints["alpha-2"].ID
	for provider, want := range map[string][]routing.EndpointID{
		`{"quantizations":["fp8"]}`:                {one},
		`{"quantizations":["fp8","fp16"]}`:         {one, two},
		`{"preferred_min_throughput":{"p50":100}}`: {one, two},
		`{"preferred_max_latency":{"p99":1}}`:      {two, one},
	} {
		if route, resp, body := r.sendWith(a, provider); resp.StatusCode != http.StatusOK || !cmp.Equal(route, want) {
			t.Errorf("%s: %d, route %v, want %v: %s", provider, resp.StatusCode, route, want, body)
		}
	}
	for _, provider := range []string{`{"quantizations":["int8"]}`, `{"quantizations":["unknown"]}`} {
		if _, resp, body := r.sendWith(a, provider); resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "override_filter") {
			t.Errorf("%s: %d %s", provider, resp.StatusCode, body)
		}
	}
	r.idle(1 << 30)
}

// TestProviderRequireParametersReadsResponsesText: Responses carries verbosity inside text, which Inspect reads only
// for format. The guarantee must see it in every format, and leave text.format to the capability filter.
func TestProviderRequireParametersReadsResponsesText(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) {
		o.endpoints = func(_ string, e *routing.Endpoint) {
			e.Parameters = allParameters()
		}
	})
	a := r.newAccount(1e9)
	for name, tc := range map[string]struct {
		path, body string
		want       int
	}{
		"messages top-level": {"/v1/messages", `{"model":"acme/alpha","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"verbosity":"high","provider":{"require_parameters":true}}`, http.StatusBadRequest},
		"chat top-level":     {"/v1/chat/completions", `{"model":"acme/alpha","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"verbosity":"high","provider":{"require_parameters":true}}`, http.StatusBadRequest},
		"responses text":     {"/v1/responses", `{"model":"acme/alpha","max_output_tokens":16,"input":"hello","text":{"verbosity":"high"},"provider":{"require_parameters":true}}`, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			resp, body := r.do(http.MethodPost, tc.path, a.key, strings.NewReader(tc.body))
			if resp.StatusCode != tc.want {
				t.Fatalf("%d, want %d: %s", resp.StatusCode, tc.want, body)
			}
			if tc.want == http.StatusBadRequest && !strings.Contains(body, "verbosity") {
				t.Errorf("refusal does not name verbosity: %s", body)
			}
		})
	}
	r.idle(1 << 30)
}

// TestProviderRefusedMembersReserveNothing: a member that cannot be kept is a 400 before anything is reserved.
func TestProviderRefusedMembersReserveNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	for _, provider := range []string{
		`{"max_price":{"image":0}}`,
	} {
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

func TestProviderErrorsAreNumeric(t *testing.T) {
	t.Parallel()
	// A request that names no tier may be served at priority, so the sale schedule prices it: these requests must reach
	// the member under test, not fail quoting the route they share.
	const bothTiers = `{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002","cache_write":"0.000001"},
		"service_tiers":{"priority":{"rates":{"input":"0.000002","output":"0.000003","cache_write":"0.000001"}}}}`
	r := newRig(t, func(o *rigOptions) { o.price = bothTiers })
	a := r.newAccount(1e9)
	for _, tc := range []struct{ provider, members, member string }{
		{`{"require_parameters":true}`, `"seed":7,`, "require_parameters"},
		{`{"max_price":{"image":0.01}}`, "", "max_price.image"},
		{`{"sort":{"by":"price","percentile":"p90"}}`, "", "sort.percentile"},
	} {
		body := `{"model":"acme/alpha",` + tc.members + `"provider":` + tc.provider + `,"messages":[{"role":"user","content":"hello"}]}`
		resp, out := r.do(http.MethodPost, "/api/v1/chat/completions", a.key, strings.NewReader(body))
		var got struct {
			Error struct {
				Code     int    `json:"code"`
				Message  string `json:"message"`
				Metadata struct {
					Type string `json:"error_type"`
				} `json:"metadata"`
			} `json:"error"`
		}
		if err := jsonx.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusBadRequest || got.Error.Code != http.StatusBadRequest || got.Error.Metadata.Type != "invalid_request" || !strings.Contains(got.Error.Message, "provider."+tc.member) {
			t.Errorf("%s: %d %s", tc.provider, resp.StatusCode, out)
		}
	}
	if r.count(allRows) != 0 || len(r.calls()) != 0 {
		t.Fatal("unsupported provider constraint reached reservation or upstream")
	}
	r.idle(1 << 30)
}
