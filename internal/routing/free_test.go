package routing_test

import (
	"errors"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/routing"
)

// freeModel is a third model, so a test can tell the ranking of two free models from the order of their ids.
var freeModel = catalog.ModelID(uuid.MustParse("00000000-0000-7000-8000-0000000000cc"))

// TestFreeRanksOnlyFreeModels: openrouter/free ignores the request's model list, considers only the :free entries the
// key may call, and ranks them by the same strategy a named model's endpoints use, so the first entry is the chosen
// model and the ones after it are its fallbacks.
func TestFreeRanksOnlyFreeModels(t *testing.T) {
	t.Parallel()
	preset := openPreset()
	preset.Strategy = routing.Cheapest
	paid := endpoint(t, 1) // the cheapest of all, and it must not be chosen
	dearFree := endpoint(t, 2, func(e *routing.Endpoint) {
		e.Model = other
		e.Free = true
		e.Price.Schedule = schedule(t, "0.000009", "0.000009", "")
	})
	cheapFree := endpoint(t, 3, func(e *routing.Endpoint) {
		e.Model = freeModel
		e.Free = true
		e.Price.Schedule = schedule(t, "0.000003", "0.000003", "")
	})
	h := newHarness(t, routing.Config{}, preset, dearFree, paid, cheapFree)

	in := input()
	in.Free = true
	in.Models = []catalog.ModelID{model} // the request's list is not what free ranks
	p := h.plan(in)
	if p.Entries[0].Model != freeModel || p.Entries[0].Endpoint != cheapFree.ID {
		t.Fatalf("free chose %v at %v, want the cheaper free model %v at %v", p.Entries[0].Model, p.Entries[0].Endpoint, freeModel, cheapFree.ID)
	}

	in.Admission.Allowed = map[catalog.ModelID]struct{}{other: {}}
	p = h.plan(in)
	if p.Entries[0].Model != other || p.Entries[0].Endpoint != dearFree.ID {
		t.Fatalf("free with a restricted key chose %v at %v, want %v at %v", p.Entries[0].Model, p.Entries[0].Endpoint, other, dearFree.ID)
	}
}

// TestFreeWithoutAFreeEntry: a snapshot with no free model leaves openrouter/free nothing to rank, which is the
// permanent no-endpoint refusal a call turns into its documented 404, not a wait.
func TestFreeWithoutAFreeEntry(t *testing.T) {
	t.Parallel()
	preset := openPreset()
	h := newHarness(t, routing.Config{}, preset, endpoint(t, 1))

	in := input()
	in.Free = true
	_, wait, err := h.r.Plan(in)
	var none *routing.NoEndpoint
	if wait != nil || !errors.As(err, &none) || !none.Permanent || len(none.Reasons) != 1 || none.Reasons[0].Reason != routing.NoCandidates {
		t.Fatalf("free with no free model: wait %v, error %v; want a permanent no_endpoint for no_candidates", wait, err)
	}
}
