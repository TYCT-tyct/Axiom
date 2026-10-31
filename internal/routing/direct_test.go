package routing_test

import (
	"testing"
	"uuid"

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

// TestCompletionsBatchAdmission: legacy completions is a batch for its input too: two prompts of 6000 tokens fit an
// 8192-token context, are admitted by the largest prompt, and keep their output reservation.
func TestCompletionsBatchAdmission(t *testing.T) {
	t.Parallel()
	schedule, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"USD","rates":{"input":"0.000001","output":"0.000002"}}`))
	if err != nil {
		t.Fatal(err)
	}
	e := endpoint(t, 1, func(e *routing.Endpoint) {
		e.Native, e.Served = routing.Of(protocol.Completions), routing.Of(protocol.Completions)
		e.Context, e.MaxOutput, e.Streams = 8192, 1, true
		e.Price = routing.PriceRef{Version: uuid.UUID(id(102)), Schedule: schedule}
	})
	in := input()
	in.Req.Format, in.Req.TextBytes, in.Req.MaxInputBytes, in.Req.Choices = protocol.Completions, 48000, 24000, 2
	plan, wait, err := newHarness(t, routing.Config{}, openPreset(), e).r.Plan(in)
	if wait != nil || err != nil {
		t.Fatalf("Plan = %v, %v", wait, err)
	}
	if len(plan.Entries) != 1 || plan.Entries[0].Target.MaxOutput != 1 {
		t.Fatalf("entries %+v, want one with its output cap", plan.Entries)
	}
}
