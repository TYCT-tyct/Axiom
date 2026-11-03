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
	t.Parallel()
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

// TestDirectRequireParameters: provider.require_parameters reads a direct request's own members as its structure, not
// as parameters no endpoint declares, so a rerank or embeddings call that asks for it is routed rather than refused.
func TestDirectRequireParameters(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		format protocol.Format
		body   string
	}{
		{protocol.Rerank, `{"model":"m","query":"q","documents":["d"],"provider":{"require_parameters":true}}`},
		{protocol.Embeddings, `{"model":"m","input":["a","b"],"provider":{"require_parameters":true}}`},
	} {
		req, err := protocol.Inspect(tc.format, []byte(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		over, _, err := readRouting(&req, []byte(tc.body))
		if err != nil || !over.RequireParameters || over.RequiredParameters != 0 {
			t.Errorf("format %d: override %+v, error %v; want require_parameters kept with nothing to declare", tc.format, over, err)
		}
	}
}
