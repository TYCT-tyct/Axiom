package gateway

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"axiom/internal/platform/jsonx"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"

	"github.com/google/go-cmp/cmp"
)

func TestOpenRouterDiscovery(t *testing.T) {
	t.Parallel()
	r := goldenRig(t)
	a := r.newAccount(1e9)
	restrict(r, a, "{acme/golden}")
	for _, path := range []string{"/api/v1/models/user", "/api/v1/models/count", "/api/v1/models/acme/golden/endpoints", "/api/v1/providers"} {
		resp, body := r.do(http.MethodGet, path, a.key, nil)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
	resp, body := r.do(http.MethodGet, "/api/v1/models/count?limit=1&q=absent", a.key, nil)
	if resp.StatusCode != http.StatusOK || body != `{"data":{"count":1}}` {
		t.Errorf("count: %d %s", resp.StatusCode, body)
	}
	user := r.orList(a, "/api/v1/models/user")
	if diff := cmp.Diff(r.orList(a, orListPath), user); diff != "" {
		t.Error(diff)
	}
	for _, path := range []string{"/api/v1/models/acme/alpha/endpoints", "/api/v1/models/no/such/endpoints"} {
		resp, body := r.do(http.MethodGet, path, a.key, nil)
		if code, _ := errorOf(t, body); resp.StatusCode != http.StatusNotFound || code != "model_not_found" {
			t.Errorf("%s: %d %s", path, resp.StatusCode, body)
		}
	}
}

func TestOpenRouterDiscoveryEmpty(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	restrict(r, a, "{}")
	for _, path := range []string{"/api/v1/models/user", "/api/v1/providers"} {
		resp, body := r.do(http.MethodGet, path, a.key, nil)
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"data":[]`) {
			t.Errorf("empty %s: %d %s", path, resp.StatusCode, body)
		}
	}
}

func TestOpenRouterUserPagination(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	page := r.orList(a, "/api/v1/models/user?limit=1")
	if page.Links.Next == nil || *page.Links.Next != "/api/v1/models/user?limit=1&offset=1" {
		t.Fatalf("user next link: %v", page.Links.Next)
	}
	next := r.orList(a, *page.Links.Next)
	if next.TotalCount != page.TotalCount || len(next.Data) != 1 || next.Data[0].ID == page.Data[0].ID {
		t.Fatalf("user next page: %+v", next)
	}
}

// Official documentation examples are recorded with their URL/date and the explicit Axiom projection. Full-response
// comparison keeps omissions and nulls reviewable; common field types are also checked against the recorded example.
func TestOpenRouterRecordedContracts(t *testing.T) {
	t.Parallel()
	r := goldenRig(t)
	a := r.newAccount(1e9)
	restrict(r, a, "{acme/golden}")
	for name, path := range map[string]string{
		"models":    orListPath,
		"user":      orListPath + "/user",
		"count":     orListPath + "/count",
		"endpoints": orListPath + "/acme/golden/endpoints",
		"providers": "/api/v1/providers",
	} {
		t.Run(name, func(t *testing.T) {
			fixture := readORRecording(t, name)
			resp, body := r.do(http.MethodGet, path, a.key, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%d %s", resp.StatusCode, body)
			}
			actual := decode(t, body)
			if diff := cmp.Diff(fixture.Axiom, actual); diff != "" {
				t.Error(diff)
			}
			checkRecordedTypes(t, "response", fixture.Response, actual)
		})
	}
}

type orRecording struct {
	Source     string `json:"source"`
	RecordedAt string `json:"recorded_at"`
	Response   any    `json:"response"`
	Axiom      any    `json:"axiom"`
}

func readORRecording(t *testing.T, name string) orRecording {
	t.Helper()
	raw, err := fs.ReadFile(os.DirFS("testdata/openrouter-models"), name+".json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture orRecording
	if err := jsonx.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Source == "" || fixture.RecordedAt == "" || fixture.Response == nil {
		t.Fatal("missing recording provenance")
	}
	return fixture
}

func checkRecordedTypes(t *testing.T, path string, recorded, actual any) {
	t.Helper()
	if recorded == nil || actual == nil {
		return
	}
	if reflect.TypeOf(recorded) != reflect.TypeOf(actual) {
		t.Errorf("%s type %T, recorded %T", path, actual, recorded)
		return
	}
	switch a := actual.(type) {
	case map[string]any:
		r := recorded.(map[string]any)
		for key, value := range a {
			checkRecordedTypes(t, path+"."+key, r[key], value)
		}
	case []any:
		r := recorded.([]any)
		if len(r) > 0 {
			for _, value := range a {
				checkRecordedTypes(t, path+"[]", r[0], value)
			}
		}
	}
}

func TestOpenRouterOffsetDefault(t *testing.T) {
	for _, tc := range []struct {
		query string
		limit int
	}{{"", 0}, {"?offset=0", orDefaultLimit}, {"?offset=", 0}, {"?offset=1&limit=2", 2}} {
		q, err := readORQuery(httptest.NewRequestWithContext(t.Context(), http.MethodGet, orListPath+tc.query, nil))
		if err != nil || q.limit != tc.limit {
			t.Errorf("%s: limit %d, %v; want %d", tc.query, q.limit, err, tc.limit)
		}
	}
}

func TestOpenRouterEndpointBuild(t *testing.T) {
	f := newRouteFixture(t)
	first, second := routable(f.served, protocol.Messages), routable(f.served, protocol.Responses)
	first.Upstream, second.Upstream = "private-first", "private-second"
	first.Codename, second.Codename = "g-first", "g-second"
	f.build([]suppliers.Routable{second, first, routable(f.nospec), routable(f.unpriced)})
	old := f.models.published()
	entry, err := old.find(visibility{}, "acme/served")
	if err != nil {
		t.Fatal(err)
	}
	before := string(entry.endpoints)
	var result struct {
		Data orEndpointModel `json:"data"`
	}
	if err := jsonx.Unmarshal(entry.endpoints, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data.Endpoints) != 2 || old.count(visibility{}, orQuery{}) != 1 {
		t.Fatalf("%s", before)
	}
	tags := []string{"g-first", "g-second"}
	for i, e := range result.Data.Endpoints {
		want := []any{tags[i], "0.0000045", int64(200000), int64(8192)}
		got := []any{e.Tag, e.Pricing.Prompt, e.ContextLength, e.MaxCompletionTokens}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Error(diff)
		}
	}
	if strings.Contains(before, "private-") {
		t.Errorf("private supplier data: %s", before)
	}

	f.build([]suppliers.Routable{first})
	latest, err := f.models.published().find(visibility{}, "acme/served")
	if err != nil {
		t.Fatal(err)
	}
	if string(latest.endpoints) == before || string(entry.endpoints) != before {
		t.Error("endpoint publication did not preserve independent builds")
	}
}

// TestDiscoveryTagIsTheRoutingIdentity: the tag a client reads from the public endpoint list is the same codename
// provider.only accepts. A client can therefore discover an endpoint and require it without a second identity.
func TestDiscoveryTagIsTheRoutingIdentity(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	resp, body := r.do(http.MethodGet, "/api/v1/models/acme/alpha/endpoints", a.key, nil)
	var got struct {
		Data orEndpointModel `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || jsonx.Unmarshal([]byte(body), &got) != nil || len(got.Data.Endpoints) != 2 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	want := map[string]routing.EndpointID{
		"alpha-1": r.endpoints["alpha-1"].ID,
		"alpha-2": r.endpoints["alpha-2"].ID,
	}
	if len(want) != len(got.Data.Endpoints) {
		t.Fatalf("endpoints: %s", body)
	}
	for _, e := range got.Data.Endpoints {
		id, ok := want[e.Tag]
		if !ok {
			t.Fatalf("tag %q is not a group codename: %s", e.Tag, body)
		}
		route, resp, body := r.sendWith(a, `{"only":["`+e.Tag+`"]}`)
		if resp.StatusCode != http.StatusOK || !cmp.Equal(route, []routing.EndpointID{id}) {
			t.Errorf("only %q: %d, route %v, want %v: %s", e.Tag, resp.StatusCode, route, []routing.EndpointID{id}, body)
		}
	}
	r.idle(1 << 30)
}
