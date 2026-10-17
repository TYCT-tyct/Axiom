package billing

import (
	"context"
	"reflect"
	"sync"
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

func (b *bank) correct(h *Hold, seq int, revision, cost int64, segments ...Segment) {
	b.t.Helper()
	if err := b.billing.Correct(b.t.Context(), Correction{Request: h.id, Seq: seq, Revision: revision, Usage: usages(segments), Cost: cost}); err != nil {
		b.t.Fatal(err)
	}
}

func twoAttempts(t *testing.T, last Segment, result Result) Outcome {
	t.Helper()
	return Outcome{Attempts: []AttemptResult{
		{Seq: 1, Cost: 5, Segments: []Segment{used(t, 50, 0, 0)}, Result: FailedBeforeCommit, Finish: FinishError},
		{Seq: 2, Cost: 7, Segments: []Segment{last}, Result: result, Finish: FinishStop},
	}}
}

// TestCorrectCosts: every attempt's cost follows its own supplier record, by its own revisions, in any state, even
// after the reservation settled or its window closed; only the charged attempt's record touches the user's charge.
func TestCorrectCosts(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour, CorrectionWindow: time.Hour})
	h := b.hold(1000)
	if _, err := b.billing.Settle(t.Context(), h, twoAttempts(t, used(t, 300, 20, 0, in), Committed)); err != nil {
		t.Fatal(err)
	}
	b.correct(h, 1, 2, 11, used(t, 900, 0, 0)) // the failed attempt: its cost, not the charge
	if charged := b.count("SELECT charged FROM billing.reservations"); charged != 320 {
		t.Errorf("the failed attempt's record charged %d, want the estimate's 320 kept", charged)
	}
	b.correct(h, 2, 1, 9, used(t, 400, 20, 0)) // the charged attempt's first revision, though attempt 1 is at 2
	b.correct(h, 2, 1, 99, used(t, 1, 0, 0))   // the same revision again: nothing
	if c1, c2, charged := b.cost(h, 1), b.cost(h, 2), b.count("SELECT charged FROM billing.reservations"); c1 != 11 || c2 != 9 || charged != 420 {
		t.Errorf("costs %d and %d, charged %d; want 11, 9 and 420", c1, c2, charged)
	}
	b.closeWindow(h)
	b.cleanup() // the window closes and the estimate settles
	b.correct(h, 2, 2, 13, used(t, 10, 0, 0))
	if c2, charged := b.cost(h, 2), b.count("SELECT charged FROM billing.reservations"); c2 != 13 || charged != 420 {
		t.Errorf("after the window: cost %d, charged %d; want 13 and 420", c2, charged)
	}
	b.balanced()
}

// TestCorrectBeforeSettle: a supplier record that arrives before a late Settle sets the cost the Settle would have
// estimated, so the attempt's cost is the record's either way round, and redelivery changes nothing.
func TestCorrectBeforeSettle(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond})
	h := b.hold(1000)
	b.supplies.journal[h.id] = JournalAttempt{Seq: 1, Key: "client:k"}
	b.cleanup()
	b.correct(h, 1, 1, 9, used(t, 400, 0, 0))
	b.correct(h, 1, 1, 9, used(t, 400, 0, 0))
	if _, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, false, used(t, 400, 0, 0))); err != nil {
		t.Fatal(err)
	}
	if c := b.cost(h, 1); c != 9 {
		t.Errorf("cost %d, want the record's 9", c)
	}
	b.balanced()
}

// TestCorrectRetriesAfterAFailure: a correction that fails on the database, before it starts or at its revision, is
// reported, not taken for a stale one, and the retry applies it.
func TestCorrectRetriesAfterAFailure(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{})
	h := b.hold(1000)
	if _, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, true, used(t, 300, 20, 0, in))); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := b.billing.Correct(cancelled, Correction{Request: h.id, Seq: 1, Revision: 1, Usage: usages([]Segment{used(t, 500, 20, 0)}), Cost: 9}); err == nil {
		t.Fatal("a correction in a cancelled context reported success")
	}
	// attempt 0 breaks the revision's CHECK: the statement that applies the revision fails
	if err := b.billing.Correct(t.Context(), Correction{Request: h.id, Seq: 0, Revision: 1, Usage: usages([]Segment{used(t, 500, 20, 0)}), Cost: 9}); err == nil {
		t.Fatal("a correction the database refused reported success")
	}
	b.correct(h, 1, 1, 9, used(t, 500, 20, 0))
	if charged := b.count("SELECT charged FROM billing.reservations"); charged != 520 {
		t.Errorf("charged %d after the retry, want 520", charged)
	}
	b.balanced()
}

// TestCorrectKeepsTheWaiver: a call waived down to its per-use items stays waived when its estimate is corrected.
func TestCorrectKeepsTheWaiver(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour, WaiverCap: 1000})
	h := b.hold(1000)
	f, err := b.billing.Settle(t.Context(), h, outcome(Unknown, FinishNone, false, used(t, 50, 20, 1, webSearch)))
	if err != nil || !f.Estimated || f.Charged != platform.AmountExact(10) {
		t.Fatalf("Settle = %+v, %v; want 10 for the search, estimated", f, err)
	}
	b.correct(h, 1, 1, 9, used(t, 500, 100, 1))
	if charged := b.count("SELECT charged FROM billing.reservations"); charged != 10 {
		t.Errorf("charged %d after the correction, want the search alone, 10", charged)
	}
	b.balanced()
}

// TestWaiverPricesPerUseItemsAtTheBand: a waived call's search is priced at the band its whole prompt falls in.
func TestWaiverPricesPerUseItemsAtTheBand(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour, WaiverCap: 1000})
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

// TestSettleUnknownUsage: usage protocol could not map is never free (docs/domains/billing.md「价目表缺项」). Tokens are
// charged at the highest token price, 1, from the first charge through a correction to the end of the window; a retry
// returns the stored Final, unknown usage included. Usage with no fallback price is charged its known part and held,
// past any window, until a supplier record prices it.
func TestSettleUnknownUsage(t *testing.T) {
	// The window is long enough that the corrections this test makes inside it cannot fall outside it on a loaded
	// machine; where a subtest needs the window to have ended, it moves it into the past itself.
	config := Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour, CorrectionWindow: time.Hour}
	settle := func(b *bank, h *Hold, s Segment) Final {
		t.Helper()
		f, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, false, s))
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	retried := func(b *bank, h *Hold, want Final) {
		t.Helper()
		if again := settle(b, h, used(t, 1, 1, 0)); !reflect.DeepEqual(again, want) {
			t.Errorf("retry = %+v, want the stored %+v", again, want)
		}
	}
	expect := func(b *bank, state string, charged int64) {
		t.Helper()
		if s, n := b.state(), b.count("SELECT charged FROM billing.reservations"); s != state || n != charged {
			t.Errorf("%s charged %d, want %s charged %d", s, n, state, charged)
		}
		b.balanced()
	}
	endWindow := func(b *bank, h *Hold) {
		t.Helper()
		b.closeWindow(h)
		b.cleanup()
	}
	t.Run("tokens, corrected", func(t *testing.T) {
		b := newBank(t, 10_000, 0, config)
		h := b.hold(1000)
		first := settle(b, h, unknown(t, 50, "usage.new_thing_tokens", "30"))
		if !first.Estimated || first.Charged != platform.AmountExact(100) {
			t.Errorf("Settle = %+v, want 100 estimated", first)
		}
		expect(b, "estimated", 100)
		retried(b, h, first)
		corrected := unknown(t, 400, "usage.new_thing_tokens", "40")
		b.correct(h, 1, 1, 9, corrected)
		expect(b, "estimated", 460)
		retried(b, h, Final{Segments: []Segment{{Usage: corrected.Usage, Reported: ^catalog.ItemSet(0)}}, Charged: platform.AmountExact(460), Estimated: true})
		endWindow(b, h)
		expect(b, "settled", 460)
	})
	t.Run("tokens, window ends", func(t *testing.T) {
		b := newBank(t, 10_000, 0, config)
		h := b.hold(1000)
		settle(b, h, unknown(t, 50, "usage.new_thing_tokens", "30"))
		endWindow(b, h)
		expect(b, "settled", 100)
	})
	unpriced := func(b *bank, h *Hold, charged int64) {
		t.Helper()
		b.closeWindow(h)
		if n, err := b.billing.Cleanup(t.Context(), 100); err != nil || n != 0 {
			t.Errorf("Cleanup = %d, %v; want nothing to end", n, err)
		}
		expect(b, "estimated", charged)
	}
	fetches := func(input int64) Segment {
		return unknown(t, input, "usage.server_tool_use.web_fetch_requests", "2")
	}
	t.Run("no fallback price", func(t *testing.T) {
		b := newBank(t, 10_000, 0, config)
		h := b.hold(1000)
		first := settle(b, h, fetches(50))
		unpriced(b, h, 70)
		retried(b, h, first)
		b.correct(h, 1, 1, 9, used(t, 400, 20, 1)) // priced at last: the window opens
		expect(b, "estimated", 430)
		endWindow(b, h)
		expect(b, "settled", 430)
	})
	t.Run("no fallback price in the record", func(t *testing.T) {
		b := newBank(t, 10_000, 0, config)
		h := b.hold(1000)
		settle(b, h, used(t, 300, 20, 0, in))
		b.correct(h, 1, 1, 9, fetches(400))
		unpriced(b, h, 420) // past the window the estimate opened
		b.correct(h, 1, 2, 9, used(t, 400, 20, 1))
		expect(b, "estimated", 430)
		endWindow(b, h)
		expect(b, "settled", 430)
	})
	t.Run("no fallback price in a cleaned-up call's record", func(t *testing.T) {
		lapsed := config
		lapsed.Lifetime = time.Millisecond
		b := newBank(t, 10_000, 0, lapsed)
		h := b.hold(1000)
		b.supplies.journal[h.id] = JournalAttempt{Seq: 1, Key: "client:abc"}
		b.cleanup()
		b.correct(h, 1, 1, 9, fetches(400))
		unpriced(b, h, 420)
		b.correct(h, 1, 2, 9, used(t, 400, 20, 1))
		endWindow(b, h)
		expect(b, "settled", 430)
	})
}

// TestSettleReturnsTheStoredFinal: a retry returns what the first Settle stored, whatever outcome it carries, and the
// estimate stays an estimate after its window closes.
func TestSettleReturnsTheStoredFinal(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Hour, CorrectionWindow: time.Hour})
	h := b.hold(1000)
	first, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, true, used(t, 300, 20, 0, in)))
	if err != nil {
		t.Fatal(err)
	}
	b.closeWindow(h)
	b.cleanup()
	again, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, true, used(t, 1, 1, 0)))
	if err != nil || !again.Estimated || again.Charged != first.Charged || len(again.Segments) != 1 || again.Segments[0].Usage.Count(in) != 300 {
		t.Errorf("retry = %+v, %v; want the first's %+v", again, err, first)
	}
}

// TestEndRequest: suppliers learn of a request's end once it is terminal, in the same transaction, and not before.
func TestEndRequest(t *testing.T) {
	b := newBank(t, 10_000, 0, Config{HolderTTL: time.Minute, ReapGrace: time.Minute, Lifetime: time.Millisecond, CorrectionWindow: time.Hour})
	settled, estimated, lost, pending := b.hold(100), b.hold(100), b.hold(100), b.hold(100)
	b.supplies.journal[pending.id] = JournalAttempt{Seq: 1, Key: "client:k"}
	if _, err := b.billing.Settle(t.Context(), settled, outcome(Committed, FinishStop, false, used(t, 50, 20, 0))); err != nil {
		t.Fatal(err)
	}
	if _, err := b.billing.Settle(t.Context(), estimated, outcome(Committed, FinishStop, true, used(t, 50, 20, 0, in))); err != nil {
		t.Fatal(err)
	}
	b.cleanup() // lost becomes cleanup_released, pending cleanup_pending
	orphan := uuid.NewV7()
	if err := b.billing.SettleOrphan(t.Context(), b.admitted, orphan); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[uuid.UUID]int{settled.id: 1, estimated.id: 0, lost.id: 1, pending.id: 0, orphan: 1} {
		if got := b.supplies.ended[id]; got != want {
			t.Errorf("%s ended %d times, want %d", id, got, want)
		}
	}
	b.closeWindow(estimated)
	b.closeWindow(pending)
	b.cleanup() // the estimate settles, the cleaned-up one is lost
	if b.supplies.ended[estimated.id] != 1 || b.supplies.ended[pending.id] != 1 {
		t.Errorf("after the window: ended %d and %d times, want once each", b.supplies.ended[estimated.id], b.supplies.ended[pending.id])
	}
}

// TestSettleRacesCorrect: a Settle and its first supplier record at once leave one cost, the record's, and a balanced
// ledger, whichever takes the lock first.
func TestSettleRacesCorrect(t *testing.T) {
	for range 3 {
		b := newBank(t, 10_000, 0, Config{})
		h := b.hold(1000)
		var wg sync.WaitGroup
		wg.Go(func() {
			if _, err := b.billing.Settle(t.Context(), h, outcome(Committed, FinishStop, true, used(t, 300, 20, 0, in))); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if err := b.billing.Correct(t.Context(), Correction{Request: h.id, Seq: 1, Revision: 1, Usage: usages([]Segment{used(t, 400, 20, 0)}), Cost: 9}); err != nil {
				t.Error(err)
			}
		})
		wg.Wait()
		if c := b.cost(h, 1); c != 9 {
			t.Errorf("cost %d, want the record's 9", c)
		}
		b.balanced()
	}
}
