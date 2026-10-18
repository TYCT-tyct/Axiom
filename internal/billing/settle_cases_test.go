package billing

import (
	"testing"
	"time"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// cost is what the books hold as the attempt's supplier cost.
func (b *bank) cost(h *Hold, seq int32) int64 {
	b.t.Helper()
	var n int64
	if err := b.app.Tx(b.t.Context(), b.platform, func(tx pg.Tx) (err error) {
		n, err = queries.AttemptCost(b.t.Context(), tx, AttemptCostParams{RequestID: h.id, AttemptSeq: seq})
		return err
	}); err != nil {
		b.t.Fatal(err)
	}
	return n
}

// TestWaiverPricesPerUseItemsAtTheBand: a waived call's search is priced at the band its whole prompt falls in.
func TestWaiverPricesPerUseItemsAtTheBand(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour})
	banded := metered(t)
	banded.Price = mustSchedule(t, `{"v":1,"currency":"USD",
		"rates":{"input":"0.000000001","output":"0.000000001","web_search":"0.00000001"},
		"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":1000,"rates":{"web_search":"0.00000002"}}]}}`)
	h := b.holdOn(banded, 3000)
	if f, err := b.billing.Settle(t.Context(), h, outcome(Unknown, FinishNone, false, used(t, 2000, 20, 1))); err != nil || f.Charged != platform.AmountExact(20) {
		t.Errorf("Settle = %+v, %v; want the search at the long-context price, 20", f, err)
	}
}

// TestSettleReleasesWithoutAttempts: a call that never reached an upstream is released once, and cleanup leaves it.
func TestSettleReleasesWithoutAttempts(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond})
	h := b.hold(1000)
	for range 2 {
		if f, err := b.billing.Settle(t.Context(), h, Outcome{}); err != nil || f.Charged != (platform.Exact{}) {
			t.Fatalf("Settle = %+v, %v", f, err)
		}
	}
	b.cleanup()
	if s, reserved := b.state(), b.count("SELECT reserved FROM billing.balances"); s != "released" || reserved != 0 || b.supplies.ended[h.id] != 1 {
		t.Errorf("%s, %d reserved, ended %d times; want released, 0, once", s, reserved, b.supplies.ended[h.id])
	}
	b.balanced()
}

// unknown is a sampling of input and 20 output, both reported, and a usage protocol could not map.
func unknown(t *testing.T, input int64, field, value string) Segment {
	t.Helper()
	u, err := catalog.NewUsage(map[catalog.Item]int64{in: input, catalog.ItemOutput: 20}, catalog.ServiceTierStandard,
		[]catalog.UnknownUsage{{Field: field, Value: value}})
	if err != nil {
		t.Fatal(err)
	}
	return Segment{Usage: u, Reported: 1<<in | 1<<catalog.ItemOutput}
}

// TestSettleReturnsTheStoredFinal: a retry returns what the first Settle stored, whatever outcome it carries, and an
// estimated charge is settled like any other, so cleanup finds nothing to move on.
func TestSettleReturnsTheStoredFinal(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond})
	h := b.hold(1000)
	first, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, true, used(t, 300, 20, 0, in)))
	if err != nil || !first.Estimated {
		t.Fatalf("Settle = %+v, %v; want an estimate", first, err)
	}
	b.cleanup()
	if s := b.state(); s != "settled" {
		t.Errorf("the estimate is %s, want settled: nothing may correct it later", s)
	}
	again, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, true, used(t, 1, 1, 0)))
	if err != nil || !again.Estimated || again.Charged != first.Charged || len(again.Segments) != 1 || again.Segments[0].Usage.Count(in) != 300 {
		t.Errorf("retry = %+v, %v; want the first's %+v", again, err, first)
	}
	b.balanced()
}

// TestEndRequest: suppliers learn of a request's end once it is terminal, in the same transaction, and not before:
// a settled call ends at once, a pending one only when cleanup releases it.
func TestEndRequest(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond})
	settled, pending := b.hold(100), b.hold(100)
	if _, err := b.billing.Settle(t.Context(), settled, outcome(Committed, FinishStop, false, used(t, 50, 20, 0))); err != nil {
		t.Fatal(err)
	}
	if ended := b.supplies.ended[pending.id]; ended != 0 {
		t.Errorf("a pending request ended %d times, want none", ended)
	}
	b.cleanup()
	orphan := uuid.NewV7()
	if err := b.billing.SettleOrphan(t.Context(), b.admitted, orphan); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[uuid.UUID]int{settled.id: 1, pending.id: 1, orphan: 1} {
		if got := b.supplies.ended[id]; got != want {
			t.Errorf("%s ended %d times, want %d", id, got, want)
		}
	}
}
