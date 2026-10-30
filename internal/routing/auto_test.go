package routing_test

import (
	"testing"

	"axiom/internal/catalog"
	"axiom/internal/routing"
)

// TestAutoRanksEveryModel: openrouter/auto ignores the request's model list and ranks every model in the snapshot, so
// the first entry is the model the router chose and the entries after it are its fallbacks.
func TestAutoRanksEveryModel(t *testing.T) {
	t.Parallel()
	preset := openPreset()
	preset.Strategy = routing.Cheapest
	cheap := endpoint(t, 1)
	dear := endpoint(t, 2, func(e *routing.Endpoint) {
		e.Model = other
		e.Price.Schedule = schedule(t, "0.000009", "0.000009", "")
	})
	h := newHarness(t, routing.Config{}, preset, dear, cheap)

	in := input()
	in.Auto = true
	in.Models = []catalog.ModelID{other} // the request's list is not what auto ranks
	p := h.plan(in)
	if p.Entries[0].Model != model || p.Entries[0].Endpoint != cheap.ID {
		t.Fatalf("auto chose %v at %v, want the cheaper model %v at %v", p.Entries[0].Model, p.Entries[0].Endpoint, model, cheap.ID)
	}

	in.Admission.Allowed = map[catalog.ModelID]struct{}{other: {}}
	p = h.plan(in)
	if p.Entries[0].Model != other || p.Entries[0].Endpoint != dear.ID {
		t.Fatalf("auto with a restricted key chose %v at %v, want %v at %v", p.Entries[0].Model, p.Entries[0].Endpoint, other, dear.ID)
	}
}
