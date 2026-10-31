package routing_test

import (
	"testing"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// TestDirectBatchAdmissionAndPrice: a direct batch is admitted by its largest input, not its total, and an embeddings
// endpoint whose sale schedule has only an input price is still routable.
func TestDirectBatchAdmissionAndPrice(t *testing.T) {
	t.Parallel()
	inputOnly, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.000001"}}`))
	if err != nil {
		t.Fatal(err)
	}
	e := endpoint(t, 1, func(e *routing.Endpoint) {
		e.Native, e.Served = routing.Of(protocol.Embeddings), routing.Of(protocol.Embeddings)
		e.Context, e.MaxOutput, e.Streams = 8192, 0, false
		e.Price = routing.PriceRef{Version: uuid.UUID(id(101)), Schedule: inputOnly}
	})
	in := input()
	in.Req.Format, in.Req.TextBytes, in.Req.MaxInputBytes = protocol.Embeddings, 48000, 24000
	in.NativeOnly, in.NoSegments = true, true
	plan, wait, err := newHarness(t, routing.Config{}, openPreset(), e).r.Plan(in)
	if wait != nil || err != nil {
		t.Fatalf("Plan = %v, %v", wait, err)
	}
	if len(plan.Entries) != 1 {
		t.Fatalf("%d entries, want one for two 6000-token inputs in an 8192-token context", len(plan.Entries))
	}
	if plan.Entries[0].Target.Format != protocol.Embeddings || plan.Entries[0].Segmentable {
		t.Errorf("entry %+v", plan.Entries[0])
	}
}

// TestDirectContextBoundary: a direct call keeps no room for output, so an input one token short of the context and
// one exactly as long fit, and one token more does not; the entry it gets has no output limit.
func TestDirectContextBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		tokens int64
		fits   bool
	}{{8191, true}, {8192, true}, {8193, false}} {
		e := endpoint(t, 1, func(e *routing.Endpoint) {
			e.Native, e.Served = routing.Of(protocol.Embeddings), routing.Of(protocol.Embeddings)
			e.Context, e.MaxOutput, e.Streams = 8192, 4096, false
		})
		in := input()
		in.Req.Format, in.Req.TextBytes = protocol.Embeddings, tc.tokens*billing.InputBytesPerToken
		in.NativeOnly, in.NoSegments = true, true
		plan, _, err := newHarness(t, routing.Config{}, openPreset(), e).r.Plan(in)
		if got := len(plan.Entries) == 1; got != tc.fits {
			t.Errorf("%d tokens in an 8192-token context: fits %t (error %v), want %t", tc.tokens, got, err, tc.fits)
			continue
		}
		if tc.fits && plan.Entries[0].Target.MaxOutput != 0 {
			t.Errorf("%d tokens: output limit %d, want 0 for a call that generates nothing", tc.tokens, plan.Entries[0].Target.MaxOutput)
		}
	}
}
