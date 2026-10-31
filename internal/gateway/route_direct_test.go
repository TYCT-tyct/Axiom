package gateway

import (
	"testing"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// TestRouteDirectOnlyModelNeedsNoGenerationSpec: a catalog model reached only through embeddings or rerank may state
// just its context length; the routing snapshot admits it with an input-only sale price, and no output limit, stream or
// ability bit is made up for it.
func TestRouteDirectOnlyModelNeedsNoGenerationSpec(t *testing.T) {
	f := newRouteFixture(t)
	id := f.seedPrice("acme/direct", `{"items":["input"],"tiers":["standard"],"context_length":8192}`,
		`{"v":1,"currency":"USD","rates":{"input":"0.0000045"}}`)
	schedule, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.0000045"}}`))
	if err != nil {
		t.Fatal(err)
	}
	f.prices[id] = billing.Sold{Version: uuid.NewV7(), Schedule: schedule}
	r := routable(id, protocol.Embeddings, protocol.Rerank)
	f.build([]suppliers.Routable{r})
	in := call(id, func(req *protocol.Request) {
		req.Format, req.TextBytes, req.MaxInputBytes, req.Stream = protocol.Embeddings, 100, 100, false
	})
	if got := f.planned(in); len(got) != 1 || got[0] != r.ID {
		t.Fatalf("routing plans to %v, want the direct-only endpoint %s", got, r.ID)
	}
}
