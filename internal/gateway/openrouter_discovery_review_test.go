package gateway

import (
	"net/http"
	"reflect"
	"slices"
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
	f := newRouteFixture(t)
	snap, err := f.cat.Current()
	if err != nil {
		t.Fatal(err)
	}
	public := routing.Endpoint{ID: routing.EndpointID(uuid.NewV7()), Model: f.served, Context: 200000, MaxOutput: 8192}
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

func TestOpenRouterEndpointTagsHideIdentifiers(t *testing.T) {
	model := orEntry{ID: "acme/model", Name: "model"}
	endpoints := []routing.Endpoint{
		{ID: routing.EndpointID(uuid.NewV7()), Context: 200000, MaxOutput: 8192},
		{ID: routing.EndpointID(uuid.NewV7()), Context: 200000, MaxOutput: 8192},
	}
	original := slices.Clone(endpoints)
	before, err := encodeOREndpoints(model, endpoints)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, endpoints) {
		t.Error("encoding mutated routing input")
	}
	for i := range endpoints {
		endpoints[i].ID = routing.EndpointID(uuid.NewV7())
	}
	slices.Reverse(endpoints)
	after, err := encodeOREndpoints(model, endpoints)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("identical public facts exposed different private IDs:\n%s\n%s", before, after)
	}
	var reply struct {
		Data orEndpointModel `json:"data"`
	}
	if err := jsonx.Unmarshal(before, &reply); err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, e := range reply.Data.Endpoints {
		tags = append(tags, e.Tag)
	}
	if diff := cmp.Diff([]string{"axiom/1", "axiom/2"}, tags); diff != "" {
		t.Error(diff)
	}
}

// TestOpenRouterEndpointPublishesQuantizationAndRequestPrice: the public endpoint shows the precision a person recorded
// and omits it when none is, and the model's price list shows a per-request price when the schedule has one.
func TestOpenRouterEndpointPublishesQuantizationAndRequestPrice(t *testing.T) {
	model := orEntry{ID: "acme/model", Name: "model", Pricing: orPricing{Request: "1.5"}}
	endpoints := []routing.Endpoint{
		{ID: routing.EndpointID(uuid.NewV7()), Context: 1, MaxOutput: 1, Quantization: "fp8"},
		{ID: routing.EndpointID(uuid.NewV7()), Context: 1, MaxOutput: 1},
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
