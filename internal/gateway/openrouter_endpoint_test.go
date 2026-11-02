package gateway

import (
	"net/http"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/jsonx"
)

// TestOpenRouterPricesEachEndpoint: a cost-plus version prices alpha-1 and alpha-2 differently. The model list and the
// public page's model price show the cheapest endpoint a call can be served by, the discovery list names each endpoint
// by its stable codename and shows its own price, and the public page lists those same endpoints at their own prices
// (OpenRouter's /models and /models/{author}/{slug}/endpoints, fetched 2026-11-02).
//
//nolint:cyclop,gocognit // the model list, endpoint list and public page in one publication
func TestOpenRouterPricesEachEndpoint(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(1e9)
	dear, err := catalog.ParseSchedule([]byte(dear))
	if err != nil {
		t.Fatal(err)
	}
	cheap := r.endpoints["alpha-1"].Price.Schedule
	r.sold[r.alpha] = billing.Sold{Version: uuid.NewV7(), Schedule: dear, Endpoints: billing.EndpointPrices{
		uuid.UUID(r.endpoints["alpha-1"].ID): cheap,
		uuid.UUID(r.endpoints["alpha-2"].ID): dear,
	}}
	r.publish()
	model := r.orModel(a, "acme/alpha")
	if model.Pricing["prompt"] != "0.000001" || model.Pricing["completion"] != "0.000002" {
		t.Errorf("the model list shows %v, want the cheapest endpoint", model.Pricing)
	}
	resp, body := r.do(http.MethodGet, "/api/v1/models/acme/alpha/endpoints", a.key, nil)
	var got struct {
		Data orEndpointModel `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || jsonx.Unmarshal([]byte(body), &got) != nil || len(got.Data.Endpoints) != 2 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	type endpointPrice = [2]string // prompt, completion
	prices := map[string]endpointPrice{}
	for _, e := range got.Data.Endpoints {
		prices[e.Tag] = endpointPrice{e.Pricing.Prompt, e.Pricing.Completion}
	}
	want := map[string]endpointPrice{
		"alpha-1": {"0.000001", "0.000002"},
		"alpha-2": {"0.000001", "0.000004"},
	}
	if !cmp.Equal(prices, want) {
		t.Errorf("the endpoint list shows %v, want each endpoint's own price by codename %v", prices, want)
	}
	var publicEndpoints map[string]string
	var public catalog.ScheduleView
	for _, m := range r.models.Public(billing.Audience{}) {
		if m.ID != "acme/alpha" {
			continue
		}
		public = m.Pricing
		publicEndpoints = make(map[string]string, len(m.Endpoints))
		for _, e := range m.Endpoints {
			for _, rate := range e.Pricing.Rates {
				if rate.Base && rate.Item == catalog.ItemOutput {
					publicEndpoints[e.Name] = rate.Price.String()
				}
			}
		}
	}
	found := false
	for _, rate := range public.Rates {
		if rate.Base && rate.Item == catalog.ItemOutput {
			found = true
			if rate.Price.String() != "0.000002" {
				t.Errorf("the public page shows output %s, want the cheapest endpoint", rate.Price)
			}
		}
	}
	if !found {
		t.Error("the public page shows no output price")
	}
	if want := map[string]string{"alpha-1": "0.000002", "alpha-2": "0.000004"}; !cmp.Equal(publicEndpoints, want) {
		t.Errorf("the public page lists endpoints %v, want %v", publicEndpoints, want)
	}
}
