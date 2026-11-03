package gateway

import (
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/jsonx"
	"axiom/internal/routing"

	"github.com/google/go-cmp/cmp"
)

func TestOpenRouterCountOutputModalities(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	restrict(r, a, "{acme/alpha}")
	for _, value := range []string{"", "text", "all", "image", "image,audio", "image,text"} {
		query := "?output_modalities=" + value
		list := r.orList(a, orListPath+query)
		resp, body := r.do(http.MethodGet, orListPath+"/count"+query, a.key, nil)
		var count struct {
			Data struct {
				Count int `json:"count"`
			} `json:"data"`
		}
		if resp.StatusCode != http.StatusOK || jsonx.Unmarshal([]byte(body), &count) != nil || count.Data.Count != list.TotalCount {
			t.Errorf("%s: count %s, list total %d", value, body, list.TotalCount)
		}
	}
	for _, query := range []string{"?output_modalities=unknown", "?output_modalities=image&output_modalities=text", "?output_modalities=%ZZ", "?output_modalities=image;limit=1"} {
		resp, body := r.do(http.MethodGet, orListPath+"/count"+query, a.key, nil)
		if code, _ := errorOf(t, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_request" {
			t.Errorf("%s: %d %s", query, resp.StatusCode, body)
		}
	}
}

func TestOpenRouterDiscoveryExcludesPrivateOwners(t *testing.T) {
	t.Parallel()
	f := newRouteFixture(t)
	snap, err := f.cat.Current()
	if err != nil {
		t.Fatal(err)
	}
	public := routing.Endpoint{ID: routing.EndpointID(uuid.NewV7()), Codename: "g-public", Model: f.served, Context: 200000, MaxOutput: 8192}
	site := routing.Owner{Site: routing.SiteID(uuid.NewV7())}
	account := routing.Owner{Account: routing.AccountID(uuid.NewV7())}
	for name, tc := range map[string]struct {
		owner  routing.Owner
		public bool
	}{
		"site-only": {site, false}, "site-shared": {site, true}, "account-only": {account, false}, "account-shared": {account, true},
	} {
		private := public
		private.ID, private.Owner, private.Context = routing.EndpointID(uuid.NewV7()), tc.owner, 123
		endpoints, want := []routing.Endpoint{private}, 0
		if tc.public {
			endpoints = append(endpoints, public)
			want = 1
		}
		l, err := newOpenRouterListing(snap, f.prices, endpoints)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(l.models); got != want {
			t.Fatalf("%s: %d discoverable models, want %d", name, got, want)
		}
		if want == 0 {
			continue
		}
		var reply struct {
			Data orEndpointModel `json:"data"`
		}
		if err := jsonx.Unmarshal(l.models[0].endpoints, &reply); err != nil {
			t.Fatal(err)
		}
		if len(reply.Data.Endpoints) != 1 || reply.Data.Endpoints[0].ContextLength != public.Context {
			t.Errorf("%s: private endpoint included: %+v", name, reply)
		}
	}
}

func TestOpenRouterEndpointTagsAreStablePublicCodenames(t *testing.T) {
	t.Parallel()
	model := orEntry{ID: "acme/model", Name: "model"}
	endpoints := []routing.Endpoint{
		{ID: routing.EndpointID(uuid.NewV7()), Codename: "g-alpha", Context: 200000, MaxOutput: 8192},
		{ID: routing.EndpointID(uuid.NewV7()), Codename: "g-beta", Context: 200000, MaxOutput: 8192},
	}
	original := slices.Clone(endpoints)
	before := mustEncodeEndpoints(t, model, endpoints)
	if !reflect.DeepEqual(original, endpoints) {
		t.Error("encoding mutated routing input")
	}
	// The tag is the group's persisted codename, the identity routing and provider filters accept.
	if diff := cmp.Diff([]string{"g-alpha", "g-beta"}, endpointTags(t, before)); diff != "" {
		t.Error(diff)
	}
	// Neither the internal id nor its ordering reaches the response: the same codenames in another input order, with
	// fresh ids, encode byte for byte the same.
	for _, e := range original {
		if strings.Contains(string(before), uuid.UUID(e.ID).String()) {
			t.Errorf("internal endpoint id in response: %s", before)
		}
	}
	for i := range endpoints {
		endpoints[i].ID = routing.EndpointID(uuid.NewV7())
	}
	slices.Reverse(endpoints)
	if after := mustEncodeEndpoints(t, model, endpoints); string(before) != string(after) {
		t.Errorf("identical public facts exposed different private IDs:\n%s\n%s", before, after)
	}
	// Adding a group never renumbers the groups already listed.
	grown := mustEncodeEndpoints(t, model, append(slices.Clone(endpoints),
		routing.Endpoint{ID: routing.EndpointID(uuid.NewV7()), Codename: "g-gamma", Context: 200000, MaxOutput: 8192}))
	if diff := cmp.Diff([]string{"g-alpha", "g-beta", "g-gamma"}, endpointTags(t, grown)); diff != "" {
		t.Error(diff)
	}
}

func mustEncodeEndpoints(t *testing.T, model orEntry, endpoints []routing.Endpoint) []byte {
	t.Helper()
	body, err := encodeOREndpoints(model, endpoints)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func endpointTags(t *testing.T, body []byte) []string {
	t.Helper()
	var reply struct {
		Data orEndpointModel `json:"data"`
	}
	if err := jsonx.Unmarshal(body, &reply); err != nil {
		t.Fatal(err)
	}
	tags := make([]string, len(reply.Data.Endpoints))
	for i, e := range reply.Data.Endpoints {
		tags[i] = e.Tag
	}
	return tags
}

// TestOpenRouterEndpointPublishesQuantizationAndRequestPrice: the public endpoint shows the precision a person recorded
// and omits it when none is, and the model's price list shows a per-request price when the schedule has one.
func TestOpenRouterEndpointPublishesQuantizationAndRequestPrice(t *testing.T) {
	t.Parallel()
	model := orEntry{ID: "acme/model", Name: "model", Pricing: orPricing{Request: "1.5"}}
	endpoints := []routing.Endpoint{
		{ID: routing.EndpointID(uuid.NewV7()), Codename: "g-fp8", Context: 1, MaxOutput: 1, Quantization: "fp8"},
		{ID: routing.EndpointID(uuid.NewV7()), Codename: "g-none", Context: 1, MaxOutput: 1},
	}
	body, err := encodeOREndpoints(model, endpoints)
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Data orEndpointModel `json:"data"`
	}
	if err := jsonx.Unmarshal(body, &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Data.Endpoints) != 2 || reply.Data.Endpoints[0].Quantization != "fp8" || reply.Data.Endpoints[1].Quantization != nil {
		t.Errorf("quantization: %+v", reply.Data.Endpoints)
	}
	if reply.Data.Endpoints[0].Pricing.Request != "1.5" {
		t.Errorf("request price: %q", reply.Data.Endpoints[0].Pricing.Request)
	}
}
