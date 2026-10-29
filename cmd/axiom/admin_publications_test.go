package main

import (
	"net/http"
	"testing"

	"axiom/internal/platform/adminapi"
)

func TestPricePolicyAPIAndUnknownCostPublication(t *testing.T) {
	f := newPreview(t)
	f.create(ruleJSON(everything, everything, faced))
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
