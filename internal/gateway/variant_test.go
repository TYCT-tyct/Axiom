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
// asked for. Variants come in any order and the last sorting one wins; a catalog variant is a 404 and never the base.
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
		"free":                                {base + ":free", modelName{}, http.StatusNotFound, ":free"},
		"batch":                               {base + ":batch", modelName{}, http.StatusNotFound, ":batch"},
		"extended":                            {base + ":extended", modelName{}, http.StatusNotFound, ":extended"},
		"thinking":                            {base + ":thinking", modelName{}, http.StatusNotFound, "reasoning parameter"},
		"a catalog variant and a routing one": {base + ":free:nitro", modelName{}, http.StatusNotFound, ":free"},
		"a suffix nobody knows":               {base + ":turbo", modelName{}, http.StatusNotFound, "not a model variant"},
		"an empty suffix":                     {base + ":", modelName{}, http.StatusNotFound, "not a model variant"},
		"an empty suffix between":             {base + "::nitro", modelName{}, http.StatusNotFound, "not a model variant"},
		"an alias":                            {"~acme/alpha-latest", modelName{}, http.StatusBadRequest, "aliases"},
		"an alias with a variant":             {"~acme/alpha-latest:nitro", modelName{}, http.StatusBadRequest, "aliases"},
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
				e.Throughput, e.Latency = 50, 100*time.Millisecond
			}
			if name == "alpha-2" {
				e.Throughput, e.Latency = 100, 5*time.Second
			}
		}
	})
	a := r.newAccount(1e9)
	one, two := r.endpoints["alpha-1"].ID, r.endpoints["alpha-2"].ID
	for name, tc := range map[string]struct {
		model, members string
		want           []routing.EndpointID
	}{
		"nitro":                             {"acme/alpha:nitro", "", []routing.EndpointID{two, one}},
		"floor":                             {"acme/alpha:floor", "", []routing.EndpointID{one, two}},
		"the last one wins":                 {"acme/alpha:floor:nitro", "", []routing.EndpointID{two, one}},
		"in the other order":                {"acme/alpha:nitro:floor", "", []routing.EndpointID{one, two}},
		"spelled loosely":                   {"ACME/Alpha:Nitro", "", []routing.EndpointID{two, one}},
		"the explicit sort replaces nitro":  {"acme/alpha:nitro", `"provider":{"sort":"price"},`, []routing.EndpointID{one, two}},
		"the explicit sort replaces floor":  {"acme/alpha:floor", `"provider":{"sort":"throughput"},`, []routing.EndpointID{two, one}},
		"an explicit sort that agrees":      {"acme/alpha:nitro", `"provider":{"sort":"throughput"},`, []routing.EndpointID{two, one}},
		"exacto, replaced by the sort":      {"acme/alpha:exacto", `"provider":{"sort":"price"},`, []routing.EndpointID{one, two}},
		"exacto, replaced by a variant":     {"acme/alpha:exacto:nitro", "", []routing.EndpointID{two, one}},
		"an alternate that ranks the same":  {"acme/alpha:nitro", `"models":["acme/beta:nitro"],`, nil},
		"an alternate that is not a model":  {"acme/alpha:nitro", `"models":["acme/none:free","acme/beta"],`, nil},
		"a variant on the alternate agrees": {"acme/alpha", `"provider":{"sort":"price"},"models":["acme/beta:floor"],`, nil},
	} {
		route, resp, body := r.sendNamed(a, tc.model, tc.members)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
			continue
		}
		if tc.want != nil && !cmp.Equal(route[:len(tc.want)], tc.want) {
			t.Errorf("%s: route %v, want it to begin %v", name, route, tc.want)
		}
		if got := r.calls()[len(r.calls())-1].Request.Model; got != tc.model {
			t.Errorf("%s: the call's model is %q, want what the client sent, %q", name, got, tc.model)
		}
	}
	r.idle(1 << 30)
}

// TestModelVariantsAreRefused: a variant that cannot be served is answered before anything is reserved or sent, and
// says why: a 400 for what OpenRouter has and this service does not, a 404 for a model that does not exist. A catalog
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
		"exacto":                   {"acme/alpha:exacto", "", 400, "invalid_request", ":exacto"},
		"exacto after a sort":      {"acme/alpha:nitro:exacto", "", 400, "invalid_request", ":exacto"},
		"online":                   {"acme/alpha:online", "", 400, "invalid_request", ":online"},
		"online with a sort":       {"acme/alpha:online", `"provider":{"sort":"price"},`, 400, "invalid_request", ":online"},
		"free":                     {"acme/alpha:free", "", 404, "model_not_found", ":free"},
		"thinking":                 {"acme/alpha:thinking", "", 404, "model_not_found", "reasoning"},
		"batch":                    {"acme/alpha:batch", "", 404, "model_not_found", ":batch"},
		"extended":                 {"acme/alpha:extended", "", 404, "model_not_found", ":extended"},
		"a suffix nobody knows":    {"acme/alpha:turbo", "", 404, "model_not_found", "not a model variant"},
		"a variant of nothing":     {"acme/none:nitro", "", 404, "model_not_found", ""},
		"an alias":                 {"~acme/alpha-latest", "", 400, "invalid_request", "aliases"},
		"an alternate's exacto":    {"acme/alpha", `"models":["acme/beta:exacto"],`, 400, "invalid_request", ":exacto"},
		"an alternate's online":    {"acme/alpha:nitro", `"models":["acme/beta:online"],`, 400, "invalid_request", ":online"},
		"an alternate's alias":     {"acme/alpha", `"models":["~acme/beta-latest"],`, 400, "invalid_request", "aliases"},
		"an alternate's sort":      {"acme/alpha", `"models":["acme/beta:nitro"],`, 400, "invalid_request", "alternate"},
		"an alternate's other":     {"acme/alpha:floor", `"models":["acme/beta:nitro"],`, 400, "invalid_request", "alternate"},
		"an alternate's other too": {"acme/alpha", `"provider":{"sort":"price"},"models":["acme/beta:nitro"],`, 400, "invalid_request", "alternate"},
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
// one it would refuse is refused alike.
func TestCountTokensReadsVariants(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(5e9)
	for model, status := range map[string]int{
		"acme/alpha:nitro": 200, "acme/alpha:floor:nitro": 200, "acme/alpha:online": 400, "acme/alpha:free": 404, "~acme/x-latest": 400,
	} {
		resp, body := r.do(http.MethodPost, "/v1/messages/count_tokens", a.key, strings.NewReader(strings.Replace(countBody, "acme/alpha", model, 1)))
		if resp.StatusCode != status {
			t.Errorf("%s: %d %s, want %d", model, resp.StatusCode, body, status)
		}
	}
}
