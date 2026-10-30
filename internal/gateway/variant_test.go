package gateway

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/routing"
)

// TestParseModelName: each variant is the routing it stands for, or the refusal it gets; the base is what the catalog is
// asked for. Variants come in any order and the last sorting one wins; an unsupported catalog variant is a 400 and never the base.
func TestParseModelName(t *testing.T) {
	t.Parallel()
	const base = "acme/alpha"
	cheapest, fastest := routing.Cheapest, routing.Fastest
	for name, tc := range map[string]struct {
		model  string
		want   modelName
		status int    // of the refusal, 0 for none
		text   string // the refusal says
	}{
		"no variant":                          {base, modelName{base: base}, 0, ""},
		"nitro":                               {base + ":nitro", modelName{base: base, sort: &fastest}, 0, ""},
		"floor":                               {base + ":floor", modelName{base: base, sort: &cheapest}, 0, ""},
		"upper case":                          {base + ":NITRO", modelName{base: base, sort: &fastest}, 0, ""},
		"the last sorting one wins":           {base + ":nitro:floor", modelName{base: base, sort: &cheapest}, 0, ""},
		"and the other way":                   {base + ":floor:nitro", modelName{base: base, sort: &fastest}, 0, ""},
		"the same twice":                      {base + ":nitro:nitro", modelName{base: base, sort: &fastest}, 0, ""},
		"exacto":                              {base + ":exacto", modelName{base: base, exacto: true}, 0, ""},
		"exacto replaced":                     {base + ":exacto:floor", modelName{base: base, sort: &cheapest}, 0, ""},
		"exacto replacing":                    {base + ":floor:exacto", modelName{base: base, exacto: true}, 0, ""},
		"online":                              {base + ":online", modelName{}, http.StatusBadRequest, ":online"},
		"online after a sort":                 {base + ":nitro:online", modelName{}, http.StatusBadRequest, ":online"},
		"free":                                {base + ":free", modelName{base: base + ":free"}, 0, ""},
		"batch":                               {base + ":batch", modelName{}, http.StatusBadRequest, ":batch"},
		"extended":                            {base + ":extended", modelName{}, http.StatusBadRequest, ":extended"},
		"thinking":                            {base + ":thinking", modelName{base: base, thinking: true}, 0, ""},
		"thinking with a sort":                {base + ":thinking:nitro", modelName{base: base, sort: &fastest, thinking: true}, 0, ""},
		"a catalog variant and a routing one": {base + ":free:nitro", modelName{base: base + ":free", sort: &fastest}, 0, ""},
		"a sorting variant then free":         {base + ":nitro:free", modelName{base: base + ":free", sort: &fastest}, 0, ""},
		"free in another case":                {base + ":FREE", modelName{base: base + ":free"}, 0, ""},
		"a suffix nobody knows":               {base + ":turbo", modelName{}, http.StatusNotFound, "not a model variant"},
		"an empty suffix":                     {base + ":", modelName{}, http.StatusNotFound, "not a model variant"},
		"an empty suffix between":             {base + "::nitro", modelName{}, http.StatusNotFound, "not a model variant"},
		"an alias":                            {"~acme/alpha-latest", modelName{base: "~acme/alpha-latest"}, 0, ""},
		"an alias with a variant":             {"~acme/alpha-latest:nitro", modelName{base: "~acme/alpha-latest", sort: &fastest}, 0, ""},
		"auto":                                {"openrouter/auto", modelName{base: "openrouter/auto", auto: true}, 0, ""},
		"auto with a variant":                 {"openrouter/auto:nitro", modelName{base: "openrouter/auto", sort: &fastest, auto: true}, 0, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, ref := parseModelName(tc.model)
			switch {
			case tc.status == 0 && ref != nil:
				t.Fatalf("refused: %v", ref.Message)
			case tc.status != 0 && (ref == nil || ref.Status != tc.status || !strings.Contains(ref.Message, tc.text)):
				t.Fatalf("got %+v, want a %d saying %q", ref, tc.status, tc.text)
			case tc.status != 0 && tc.status == http.StatusNotFound && ref.Code != "model_not_found":
				t.Fatalf("a missing model is %q", ref.Code)
			}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(modelName{})); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

// TestModelVariantsRoute: through the HTTP entry, a variant sets the ranking exactly as the provider.sort it stands for
// does, an explicit provider.sort replaces it, and the model the call records is the name the client sent. The
// endpoints rank every way differently: alpha-2 is the faster but dearer, alpha-1 the cheaper.
func TestModelVariantsRoute(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) {
		o.endpoints = func(name string, e *routing.Endpoint) {
			if name == "alpha-1" {
				e.Throughput, e.Latency, e.Quality = 50, 100*time.Millisecond, .5
			}
			if name == "alpha-2" {
				e.Throughput, e.Latency, e.Quality = 100, 5*time.Second, 1
			}
		}
	})
	a := r.newAccount(1e9)
	one, two, three := r.endpoints["alpha-1"].ID, r.endpoints["alpha-2"].ID, r.endpoints["beta-1"].ID
	for name, tc := range map[string]struct {
		model, members string
		want           []routing.EndpointID
	}{
		"nitro":                                      {"acme/alpha:nitro", "", []routing.EndpointID{two, one}},
		"floor":                                      {"acme/alpha:floor", "", []routing.EndpointID{one, two}},
		"the last one wins":                          {"acme/alpha:floor:nitro", "", []routing.EndpointID{two, one}},
		"in the other order":                         {"acme/alpha:nitro:floor", "", []routing.EndpointID{one, two}},
		"spelled loosely":                            {"ACME/Alpha:Nitro", "", []routing.EndpointID{two, one}},
		"the explicit sort replaces nitro":           {"acme/alpha:nitro", `"provider":{"sort":"price"},`, []routing.EndpointID{one, two}},
		"the explicit sort replaces floor":           {"acme/alpha:floor", `"provider":{"sort":"throughput"},`, []routing.EndpointID{two, one}},
		"an explicit sort that agrees":               {"acme/alpha:nitro", `"provider":{"sort":"throughput"},`, []routing.EndpointID{two, one}},
		"exacto, quality first":                      {"acme/alpha:exacto", "", []routing.EndpointID{two, one}},
		"exacto, replaced by the sort":               {"acme/alpha:exacto", `"provider":{"sort":"price"},`, []routing.EndpointID{one, two}},
		"exacto, replaced by a variant":              {"acme/alpha:exacto:nitro", "", []routing.EndpointID{two, one}},
		"thinking, which the request asks for":       {"acme/alpha:thinking", `"output_config":{"effort":"high"},`, []routing.EndpointID{one, two}},
		"an alternate that ranks the same":           {"acme/alpha:nitro", `"models":["acme/beta:nitro"],`, []routing.EndpointID{two, one, three}},
		"an alternate that is not a model":           {"acme/alpha:nitro", `"models":["acme/none:turbo","acme/beta"],`, []routing.EndpointID{two, one, three}},
		"a variant on the alternate agrees":          {"acme/alpha", `"provider":{"sort":"price"},"models":["acme/beta:floor"],`, []routing.EndpointID{one, two, three}},
		"an alternate exacto that matches":           {"acme/alpha:exacto", `"models":["acme/beta:exacto"],`, []routing.EndpointID{two, one, three}},
		"an alternate exacto, replaced by the sort":  {"acme/alpha", `"provider":{"sort":"price"},"models":["acme/beta:exacto"],`, []routing.EndpointID{one, two, three}},
		"an alternate exacto, replaced by a variant": {"acme/alpha:nitro", `"models":["acme/beta:exacto:nitro"],`, []routing.EndpointID{two, one, three}},
		"the most variants a name can hold":          {"acme/alpha" + strings.Repeat(":nitro", 31), "", []routing.EndpointID{two, one}},
	} {
		route, resp, body := r.sendNamed(a, tc.model, tc.members)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
			continue
		}
		if !cmp.Equal(route, tc.want) {
			t.Errorf("%s: route %v, want %v", name, route, tc.want)
		}
		if got := r.calls()[len(r.calls())-1].Request.Model; got != tc.model {
			t.Errorf("%s: the call's model is %q, want what the client sent, %q", name, got, tc.model)
		}
	}
	r.idle(1 << 30)
}

// TestModelVariantsAreRefused: a variant that cannot be served is answered before anything is reserved or sent, and
// says why: a 400 for what OpenRouter has and this service does not, a 404 for an unknown suffix or model. A catalog
// variant is not served by its base model, which exists.
func TestModelVariantsAreRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	for name, tc := range map[string]struct {
		model, members string
		status         int
		code, text     string
	}{
		"online":                   {"acme/alpha:online", "", 400, "invalid_request", ":online"},
		"online with a sort":       {"acme/alpha:online", `"provider":{"sort":"price"},`, 400, "invalid_request", ":online"},
		"free":                     {"acme/alpha:free", "", 404, "model_not_found", ""},
		"thinking":                 {"acme/alpha:thinking", "", 400, "invalid_request", "reasoning parameter"},
		"thinking, reasoning off":  {"acme/alpha:thinking", `"thinking":{"type":"disabled"},`, 400, "invalid_request", "reasoning parameter"},
		"batch":                    {"acme/alpha:batch", "", 400, "invalid_request", ":batch"},
		"extended":                 {"acme/alpha:extended", "", 400, "invalid_request", ":extended"},
		"a suffix nobody knows":    {"acme/alpha:turbo", "", 404, "model_not_found", "not a model variant"},
		"a variant of nothing":     {"acme/none:nitro", "", 404, "model_not_found", ""},
		"an alias":                 {"~acme/alpha-latest", "", 404, "model_not_found", ""},
		"an alternate's exacto":    {"acme/alpha", `"models":["acme/beta:exacto"],`, 400, "invalid_request", "alternate"},
		"an alternate's thinking":  {"acme/alpha", `"models":["acme/beta:thinking"],`, 400, "invalid_request", "reasoning"},
		"an alternate's online":    {"acme/alpha:nitro", `"models":["acme/beta:online"],`, 400, "invalid_request", ":online"},
		"an alternate's sort":      {"acme/alpha", `"models":["acme/beta:nitro"],`, 400, "invalid_request", "alternate"},
		"an alternate's other":     {"acme/alpha:floor", `"models":["acme/beta:nitro"],`, 400, "invalid_request", "alternate"},
		"an alternate's other too": {"acme/alpha", `"provider":{"sort":"price"},"models":["acme/beta:nitro"],`, 400, "invalid_request", "alternate"},
		"an alternate's exacto, whatever the first name ranks": {"acme/alpha:nitro", `"models":["acme/beta:exacto"],`, 400, "invalid_request", "alternate"},
		"an alternate's exacto after the first name's floor":   {"acme/alpha:floor", `"models":["acme/alpha:exacto"],`, 400, "invalid_request", "alternate"},
		"a name longer than a record holds":                    {"acme/alpha" + strings.Repeat(":nitro", 32), "", 404, "model_not_found", "longer than"},
		"a long name of a base nothing has":                    {strings.Repeat("a", 201), "", 404, "model_not_found", "longer than"},
	} {
		_, resp, body := r.sendNamed(a, tc.model, tc.members)
		if code, _ := errorOf(t, body); resp.StatusCode != tc.status || code != tc.code || !strings.Contains(body, tc.text) {
			t.Errorf("%s: %d %s, want %d %s saying %q", name, resp.StatusCode, body, tc.status, tc.code, tc.text)
		}
	}
	if r.count(allRows) != 0 || len(r.calls()) != 0 {
		t.Errorf("%d reservation rows, %d calls", r.count(allRows), len(r.calls()))
	}
	r.idle(1 << 30)
}

// TestCountTokensReadsVariants: counting reads the model as a call does, so a name that a call would serve is counted and
// one it would refuse is refused alike, an :exacto that only an explicit provider.sort replaces included.
func TestCountTokensReadsVariants(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(5e9)
	for name, tc := range map[string]struct {
		model, members string
		status         int
	}{
		"nitro":                           {"acme/alpha:nitro", "", 200},
		"the last variant wins":           {"acme/alpha:floor:nitro", "", 200},
		"online":                          {"acme/alpha:online", "", 400},
		"free":                            {"acme/alpha:free", "", 404},
		"an alias":                        {"~acme/x-latest", "", 404},
		"exacto":                          {"acme/alpha:exacto", "", 200},
		"exacto, replaced by the sort":    {"acme/alpha:exacto", `"provider":{"sort":"price"},`, 200},
		"exacto, replaced by a variant":   {"acme/alpha:exacto:nitro", "", 200},
		"thinking":                        {"acme/alpha:thinking", "", 400},
		"thinking, which the count asks":  {"acme/alpha:thinking", `"output_config":{"effort":"high"},`, 200},
		"an alternate's exacto":           {"acme/alpha:nitro", `"models":["acme/beta:exacto"],`, 400},
		"an alternate's exacto, replaced": {"acme/alpha", `"provider":{"sort":"price"},"models":["acme/beta:exacto"],`, 200},
		"a provider selector":             {"acme/alpha", `"provider":{"only":["alpha-1"]},`, 200},
		"a latency sort":                  {"acme/alpha", `"provider":{"sort":"latency"},`, 200},
		"a provider a call would refuse":  {"acme/alpha", `"provider":{"quantizations":["fp8"]},`, 400},
		"parameters a call would declare": {"acme/alpha", `"provider":{"require_parameters":true},`, 200},
		"parameters a call would not":     {"acme/alpha", `"seed":7,"provider":{"require_parameters":true},`, 400},
		"a name too long to be recorded":  {"acme/alpha" + strings.Repeat(":nitro", 32), "", 404},
	} {
		body := strings.Replace(strings.Replace(countBody, "acme/alpha", tc.model, 1), `{"model"`, `{`+tc.members+`"model"`, 1)
		if resp, out := r.do(http.MethodPost, "/v1/messages/count_tokens", a.key, strings.NewReader(body)); resp.StatusCode != tc.status {
			t.Errorf("%s: %d %s, want %d", name, resp.StatusCode, out, tc.status)
		}
	}
}

// TestAnEmptyEffortCannotSatisfyThinking: a Chat request whose only reasoning setting is an empty effort asks for no
// reasoning — the conversion writes no thinking for it — so :thinking on the model or on an alternate is refused
// instead of being served by the plain model.
func TestAnEmptyEffortCannotSatisfyThinking(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	for name, body := range map[string]string{
		"the model":    `{"model":"acme/alpha:thinking","max_tokens":1000,"reasoning_effort":"","messages":[{"role":"user","content":"hello"}]}`,
		"an alternate": `{"model":"acme/beta","models":["acme/alpha:thinking"],"max_tokens":1000,"reasoning_effort":"","messages":[{"role":"user","content":"hello"}]}`,
	} {
		resp, out := r.do(http.MethodPost, "/v1/chat/completions", a.key, strings.NewReader(body))
		if code, _ := errorOf(t, out); resp.StatusCode != http.StatusBadRequest || code != "invalid_request" ||
			!strings.Contains(out, "reasoning parameter") {
			t.Errorf("%s: %d %s", name, resp.StatusCode, out)
		}
	}
	if calls := r.calls(); len(calls) != 0 {
		t.Fatalf("%d calls reached an upstream", len(calls))
	}
	r.idle(1 << 30)
}

// TestAModelNameTooLongToBeRecordedIsRefusedFirst: the call record holds the name the client wrote, whole, in at most 200
// characters, so a short base with variants stacked past that is refused at the door like a long base always was: before
// anything is reserved, charged or sent, not served and then lost from the record.
func TestAModelNameTooLongToBeRecordedIsRefusedFirst(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	for _, model := range []string{"acme/alpha" + strings.Repeat(":nitro", 32), "acme/alpha" + strings.Repeat(":floor:nitro", 100)} {
		route, resp, body := r.sendNamed(a, model, "")
		if code, _ := errorOf(t, body); resp.StatusCode != http.StatusNotFound || code != "model_not_found" || len(route) != 0 {
			t.Errorf("%d bytes: %d %s, route %v", len(model), resp.StatusCode, body, route)
		}
	}
	if r.count(allRows) != 0 || len(r.calls()) != 0 || r.count("SELECT balance FROM billing.balances WHERE account_id = $1", a.id) != 1e9 {
		t.Error("a reservation, a call or a charge was made for a name that was refused")
	}
	r.idle(1 << 30)
}

// TestAnAlternateWithAVariantServesWhenTheFirstModelCannot: the alternate with a variant is not only listed, it takes the
// call when the first model has no endpoint left, and it is the only entry of the route.
func TestAnAlternateWithAVariantServesWhenTheFirstModelCannot(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) {
		o.endpoints = func(name string, e *routing.Endpoint) {
			if strings.HasPrefix(name, "alpha") {
				e.Enabled = false
			}
		}
	})
	a := r.newAccount(1e9)
	beta := []routing.EndpointID{r.endpoints["beta-1"].ID}
	for name, tc := range map[string]struct {
		model, members string
		status         int
		want           []routing.EndpointID
	}{
		"no alternate":                  {"acme/alpha:nitro", "", http.StatusNotFound, nil},
		"an alternate with a variant":   {"acme/alpha:nitro", `"models":["acme/beta:nitro"],`, http.StatusOK, beta},
		"an alternate that is no model": {"acme/alpha:nitro", `"models":["acme/beta:free"],`, http.StatusNotFound, nil},
		"an alternate that is an alias": {"acme/alpha:nitro", `"models":["~acme/beta-latest"],`, http.StatusNotFound, nil},
	} {
		route, resp, body := r.sendNamed(a, tc.model, tc.members)
		if resp.StatusCode != tc.status || !cmp.Equal(route, tc.want) {
			t.Errorf("%s: %d %s, route %v, want %d %v", name, resp.StatusCode, body, route, tc.status, tc.want)
		}
	}
	r.idle(1 << 30)
}

// TestExactoRoutesOnlyVerifiedEndpoints: :exacto keeps only endpoints quality verified as the model they claim, and
// refuses with the documented 503 when none is, naming the reason, instead of falling back to an unverified endpoint.
func TestExactoRoutesOnlyVerifiedEndpoints(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) {
		o.endpoints = func(name string, e *routing.Endpoint) {
			e.Verified = name == "alpha-2" || name == "beta-1"
		}
	})
	a := r.newAccount(1e9)
	route, resp, body := r.sendNamed(a, "acme/alpha:exacto", `"models":["acme/beta:exacto"],`)
	if want := []routing.EndpointID{r.endpoints["alpha-2"].ID, r.endpoints["beta-1"].ID}; resp.StatusCode != http.StatusOK || !cmp.Equal(route, want) {
		t.Fatalf("%d %s, route %v, want %v", resp.StatusCode, body, route, want)
	}

	none := newRig(t, func(o *rigOptions) {
		o.endpoints = func(name string, e *routing.Endpoint) { e.Verified = false }
	})
	b := none.newAccount(1e9)
	route, resp, body = none.sendNamed(b, "acme/alpha:exacto", "")
	if code, _ := errorOf(t, body); resp.StatusCode != http.StatusServiceUnavailable || code != "no_endpoint" ||
		!strings.Contains(body, "not_verified") || len(route) != 0 {
		t.Fatalf("%d %s, route %v; want a 503 no_endpoint naming not_verified", resp.StatusCode, body, route)
	}
	if none.count(allRows) != 0 || len(none.calls()) != 0 {
		t.Fatal("an unverified :exacto reserved or called")
	}
	r.idle(1 << 30)
	none.idle(1 << 30)
}
