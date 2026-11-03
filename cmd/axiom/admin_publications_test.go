package main

import (
	"net/http"
	"testing"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/adminapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func TestPricePolicyAPIAndUnknownCostPublication(t *testing.T) {
	t.Parallel()
	f := newPreview(t)
	f.create(ruleJSON(everything, everything, faced))
	seedUnpricedPublication(t, f.adminFixture)

	rec := f.do(http.MethodGet, "/pricing/publications", "", as(f.s))
	expectAdmin(t, "publication list", rec, http.StatusOK, "")
	var state adminapi.PricePublicationList
	decode(t, rec, &state)
	if state.Policy == nil || state.Policy.Rev == nil {
		t.Fatal("missing policy")
	}
	rec = f.change(f.s, http.MethodPut, "/pricing/policy", `{"rev":0,"floor_multiplier":"0.9","change_ratio":"0.1","max_increase_ratio":"1","reason":"invalid floor"}`, newKey())
	expectAdmin(t, "invalid floor", rec, http.StatusUnprocessableEntity, codeUnprocessable)
	fields := errorDetailOf(t, rec).Fields
	if fields == nil || len(*fields) != 1 || (*fields)[0].Pointer != "/floor_multiplier" {
		t.Fatalf("policy field pointer: %v", fields)
	}
	input := `{"rev":0,"floor_multiplier":"1.2","change_ratio":"0.1","max_increase_ratio":"1","reason":"site floor"}`
	key := newKey()
	for range 2 {
		rec = f.change(f.s, http.MethodPut, "/pricing/policy", input, key)
		expectAdmin(t, "idempotent policy", rec, http.StatusOK, "")
	}
	rec = f.change(f.s, http.MethodPut, "/pricing/policy", input, newKey())
	expectAdmin(t, "stale policy", rec, http.StatusConflict, codeStaleRevision)
	// No configured endpoint supplies cost evidence for this catalog model.
	rec = f.do(http.MethodGet, "/pricing/publications", "", as(f.s))
	expectAdmin(t, "unpriced list", rec, http.StatusOK, "")
	decode(t, rec, &state)
	if state.Data == nil || len(*state.Data) == 0 {
		t.Fatal("missing model proposal")
	}
	p := (*state.Data)[0]
	if p.Status == nil || string(*p.Status) != "unpriced" {
		t.Fatalf("unknown cost proposal: %+v", p)
	}
	if p.CandidateId == nil {
		t.Fatal("candidate not identified")
	}
	rec = f.change(f.s, http.MethodPost, "/pricing/publications/"+f.model, `{"candidate_id":"`+*p.CandidateId+`","confirmed":true,"override_floor":true,"reason":"cannot override unknown cost"}`, newKey())
	expectAdmin(t, "unknown cost override", rec, http.StatusUnprocessableEntity, codeUnprocessable)
}

func TestPricePublicationConversionKeepsEndpointPrices(t *testing.T) {
	t.Parallel()

	schedule, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.000002"}}`))
	if err != nil {
		t.Fatal(err)
	}
	endpoint := uuid.NewV7()
	prices := billing.EndpointPrices{endpoint: schedule}
	p := billing.Publication{
		Model:              catalog.ModelID(uuid.NewV7()),
		Endpoints:          prices,
		RequestedEndpoints: prices,
		CurrentEndpoints:   prices,
		CurrentSchedule:    jsonx.Raw(`{"v":1,"currency":"USD","rates":{"input":"0.000002"}}`),
		Costs:              prices,
		Floors:             prices,
	}
	out, err := converted[adminapi.PricePublicationList](struct {
		Policy billing.PricePolicy   `json:"policy"`
		Data   []billing.Publication `json:"data"`
	}{billing.PricePolicy{}, []billing.Publication{p}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Data == nil || len(*out.Data) != 1 {
		t.Fatalf("publication conversion returned %+v", out.Data)
	}
	row := (*out.Data)[0]
	if row.CurrentSchedule == nil || row.CurrentSchedule.Currency != "USD" {
		t.Fatalf("current schedule did not survive the publication conversion: %+v", row.CurrentSchedule)
	}
	for name, prices := range map[string]*adminapi.PriceEndpointMap{
		"endpoints":           row.Endpoints,
		"requested_endpoints": row.RequestedEndpoints,
		"current_endpoints":   row.CurrentEndpoints,
		"costs":               row.Costs,
		"floors":              row.Floors,
	} {
		if prices == nil || (*prices)[endpoint.String()].Currency != "USD" {
			t.Fatalf("%s did not survive the publication conversion: %+v", name, prices)
		}
	}
}

func seedUnpricedPublication(t *testing.T, f adminFixture) {
	t.Helper()
	site, ok := f.svc.MainSite()
	if !ok {
		t.Fatal("missing main site")
	}
	if err := f.db.Tx(t.Context(), pg.PlatformScope(), func(tx pg.Tx) error { return refreshSalePrices(t.Context(), tx, uuid.UUID(site.ID), site.Currency) }); err != nil {
		t.Fatal(err)
	}
}
