package billing

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/google/go-cmp/cmp"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// batchRow is a batch as the stored state has it, with the sequence number of its credit transaction.
func batchRow(seq, amount int64, source string, expiresAt *time.Time) UnspentBatchesRow {
	return UnspentBatchesRow{ID: uuid.NewV7(), Source: source, ExpiresAt: expiresAt, Amount: amount, Seq: seq}
}

func after(d time.Duration) *time.Time { t := epoch.Add(d); return &t }

func creditOf(r UnspentBatchesRow) movement {
	return movement{seq: r.Seq, kind: "credit", request: r.ID, debit: -r.Amount}
}

// leftOf is what each batch has left, by position in rows.
func leftOf(a *allocation, rows []UnspentBatchesRow) []int64 {
	left := make([]int64, len(rows))
	for i, r := range rows {
		left[i] = a.batches[r.ID].remaining()
	}
	return left
}

// TestSpendOrder: a charge takes the batch that expires first, a batch that does not expire last; of the same expiry a
// gift before paid credit, then the older; what no batch holds is debt that the next credit pays off first.
func TestSpendOrder(t *testing.T) {
	rows := []UnspentBatchesRow{
		batchRow(1, 100, "gift", nil),
		batchRow(2, 100, "paid", after(time.Hour)),
		batchRow(3, 100, "gift", after(time.Hour)),
		batchRow(4, 100, "gift", after(2*time.Hour)),
		batchRow(5, 100, "gift", after(time.Hour)),
	}
	for _, tc := range []struct {
		name  string
		spend []int64
		left  []int64 // by row
		debt  int64
	}{
		{"nothing", nil, []int64{100, 100, 100, 100, 100}, 0},
		{"the older gift of the earliest expiry first", []int64{50}, []int64{100, 100, 50, 100, 100}, 0},
		{"then the other gift, then the paid", []int64{250}, []int64{100, 50, 0, 100, 0}, 0},
		{"then later expiries, then the batch that never expires", []int64{450}, []int64{50, 0, 0, 0, 0}, 0},
		{"beyond everything is debt", []int64{600}, []int64{0, 0, 0, 0, 0}, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAllocation(AllocationStateRow{AllocatedSeq: 5}, rows)
			for i, n := range tc.spend {
				if err := a.apply(movement{seq: int64(6 + i), kind: "settle", request: uuid.NewV7(), debit: n}); err != nil {
					t.Fatal(err)
				}
			}
			if diff := cmp.Diff(tc.left, leftOf(a, rows)); diff != "" || a.unfunded != tc.debt {
				t.Errorf("left %v (-want +got):\n%s\nunfunded %d, want %d", leftOf(a, rows), diff, a.unfunded, tc.debt)
			}
		})
	}
}

// TestDebtIsPaidByTheNextCredit: the balance counts a debt against the credit that follows, so the batch opens with
// that much already consumed, and one the debt eats whole opens empty.
func TestDebtIsPaidByTheNextCredit(t *testing.T) {
	small, large := batchRow(2, 30, "gift", nil), batchRow(3, 100, "gift", nil)
	a := newAllocation(AllocationStateRow{}, []UnspentBatchesRow{small, large})
	for _, m := range []movement{
		{seq: 1, kind: "settle", request: uuid.NewV7(), debit: 50}, creditOf(small), creditOf(large),
	} {
		if err := a.apply(m); err != nil {
			t.Fatal(err)
		}
	}
	if got := leftOf(a, []UnspentBatchesRow{small, large}); !slices.Equal(got, []int64{0, 80}) || a.unfunded != 0 || a.funded() != 80 {
		t.Errorf("left %v, unfunded %d, funded %d; want [0 80], 0, 80", got, a.unfunded, a.funded())
	}
}

// TestGiveBack: a charge given back pays the debt first; what is left belongs to no batch, is spent after every batch,
// and does not revive one that was written off.
func TestGiveBack(t *testing.T) {
	gift := batchRow(1, 100, "gift", after(time.Hour))
	a := newAllocation(AllocationStateRow{}, []UnspentBatchesRow{gift})
	for _, m := range []movement{
		creditOf(gift),
		{seq: 2, kind: "settle", request: uuid.NewV7(), debit: 130},  // 30 of debt
		{seq: 3, kind: "reverse", request: uuid.NewV7(), debit: -50}, // 30 off the debt, 20 outside the batches
		{seq: 4, kind: "settle", request: uuid.NewV7(), debit: 5},
	} {
		if err := a.apply(m); err != nil {
			t.Fatal(err)
		}
	}
	if a.unfunded != 0 || a.unbatched != 15 || a.batches[gift.ID].remaining() != 0 || a.funded() != 15 {
		t.Errorf("unfunded %d, unbatched %d, batch %d, funded %d; want 0, 15, 0, 15", a.unfunded, a.unbatched, a.batches[gift.ID].remaining(), a.funded())
	}
}

// TestApplyRefusesWhatTheLedgerCannotHold: a replay that meets a movement out of order, an expiry larger than what its
// batch holds, or a credit that is not its batch stops instead of making up an allocation.
func TestApplyRefusesWhatTheLedgerCannotHold(t *testing.T) {
	gift := batchRow(1, 100, "gift", after(time.Hour))
	for name, ms := range map[string][]movement{
		"out of order":         {creditOf(gift), creditOf(gift)},
		"a credit of another":  {{seq: 1, kind: "credit", request: gift.ID, debit: -99}},
		"a credit unknown":     {{seq: 1, kind: "credit", request: uuid.NewV7(), debit: -100}},
		"an expiry too large":  {creditOf(gift), {seq: 2, kind: "expire", request: gift.ID, debit: 101}},
		"an expiry of nothing": {creditOf(gift), {seq: 2, kind: "expire", request: gift.ID, debit: 0}},
	} {
		t.Run(name, func(t *testing.T) {
			a := newAllocation(AllocationStateRow{}, []UnspentBatchesRow{gift})
			var err error
			for _, m := range ms {
				if err = a.apply(m); err != nil {
					break
				}
			}
			if err == nil {
				t.Error("the movements were replayed")
			}
		})
	}
}

// snapshot is what save stores of a: the state, and the batches that still hold something or have not been credited.
func snapshot(a *allocation) (AllocationStateRow, []UnspentBatchesRow) {
	state := AllocationStateRow{AllocatedSeq: a.seq, Unfunded: a.unfunded, Unbatched: a.unbatched}
	var rows []UnspentBatchesRow
	for _, b := range a.batches {
		if b.remaining() > 0 {
			rows = append(rows, UnspentBatchesRow{
				ID: b.id, SiteID: b.site, Source: b.source, ExpiresAt: b.expiresAt, Amount: b.amount,
				Consumed: b.consumed, Expired: b.expired, Seq: b.opened,
			})
		}
	}
	return state, rows
}

// pristine is the batches as the credit transactions made them, before any replay.
func pristine(rows []UnspentBatchesRow) []UnspentBatchesRow {
	out := slices.Clone(rows)
	for i := range out {
		out[i].Consumed, out[i].Expired = 0, 0
	}
	return out
}

// randomBatches are the batches a property run credits, not yet credited: their sequence numbers are set when the
// credit is posted.
func randomBatches(r *rand.Rand) []UnspentBatchesRow {
	rows := make([]UnspentBatchesRow, 1+r.IntN(5))
	for i := range rows {
		var expires *time.Time
		if r.IntN(3) > 0 {
			expires = after(time.Duration(r.IntN(4)) * time.Hour)
		}
		rows[i] = batchRow(1<<40, 1+r.Int64N(200), []string{"gift", "paid"}[r.IntN(2)], expires)
	}
	return rows
}

// randomMovement is the next movement of a run: a credit of the next batch, a write-off of part of an open batch, a
// charge given back, or a charge.
func randomMovement(r *rand.Rand, live *allocation, rows []UnspentBatchesRow, next *int, seq int64) movement {
	switch n := r.IntN(10); {
	case n < 2 && *next < len(rows):
		rows[*next].Seq = seq
		live.batches[rows[*next].ID].opened = seq
		*next++
		return creditOf(rows[*next-1])
	case n < 4 && len(live.open) > 0:
		b := live.open[r.IntN(len(live.open))]
		return movement{seq: seq, kind: "expire", request: b.id, debit: 1 + r.Int64N(b.remaining())}
	case n < 5:
		return movement{seq: seq, kind: "reverse", request: uuid.NewV7(), debit: -(1 + r.Int64N(80))}
	default:
		return movement{seq: seq, kind: "settle", request: uuid.NewV7(), debit: 1 + r.Int64N(120)}
	}
}

// checkInvariants fails the test unless the allocation funds exactly the balance, no batch is over-spent, and debt is
// held only while no batch holds anything.
func checkInvariants(t *testing.T, live *allocation, balance int64, m movement) {
	t.Helper()
	if live.funded() != balance {
		t.Fatalf("after %+v the allocation funds %d of a balance of %d", m, live.funded(), balance)
	}
	for _, b := range live.batches {
		if b.consumed < 0 || b.expired < 0 || b.consumed+b.expired > b.amount {
			t.Fatalf("batch %+v over-spent", *b)
		}
	}
	if live.unfunded > 0 && (len(live.open) > 0 || live.unbatched > 0) {
		t.Fatalf("debt %d with batches open (%d) or credit outside them (%d)", live.unfunded, len(live.open), live.unbatched)
	}
}

// TestReplayProperty: over random credits, charges, write-offs and returns, the allocation always funds exactly the
// balance, never over-spends a batch, and holds debt only while no batch holds anything; and replaying from nothing,
// or resuming from what was stored at any point, ends where the live replay did.
func TestReplayProperty(t *testing.T) {
	for seed := range uint64(200) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(seed, ^seed)) //nolint:gosec // G404: test data
			rows := randomBatches(r)
			live := newAllocation(AllocationStateRow{}, pristine(rows))
			var log []movement
			var balance int64
			next := 0
			for seq := int64(1); seq <= 60; seq++ {
				m := randomMovement(r, live, rows, &next, seq)
				if err := live.apply(m); err != nil {
					t.Fatalf("movement %+v: %v", m, err)
				}
				log = append(log, m)
				balance -= m.debit
				checkInvariants(t, live, balance, m)
			}
			checkReplays(t, r, rows, log, live)
		})
	}
}

// checkReplays replays log from nothing, and again with a stop at a random point where the state is stored and loaded
// anew; each must end where live did.
func checkReplays(t *testing.T, r *rand.Rand, rows []UnspentBatchesRow, log []movement, live *allocation) {
	t.Helper()
	fresh := newAllocation(AllocationStateRow{}, pristine(rows))
	resumed := newAllocation(AllocationStateRow{}, pristine(rows))
	stop := r.IntN(len(log) + 1)
	for i, m := range log {
		if err := fresh.apply(m); err != nil {
			t.Fatal(err)
		}
		if i == stop {
			resumed = newAllocation(snapshot(resumed))
		}
		if err := resumed.apply(m); err != nil {
			t.Fatal(err)
		}
	}
	sameAllocation(t, "from nothing", fresh, live)
	sameAllocation(t, "resumed", resumed, live)
}

func sameAllocation(t *testing.T, name string, got, live *allocation) {
	t.Helper()
	if got.unfunded != live.unfunded || got.unbatched != live.unbatched || got.seq != live.seq {
		t.Errorf("%s: unfunded %d unbatched %d seq %d, live %d %d %d", name, got.unfunded, got.unbatched, got.seq, live.unfunded, live.unbatched, live.seq)
	}
	for id, b := range live.batches {
		g, ok := got.batches[id]
		switch {
		case !ok && b.remaining() != 0: // a batch spent before the stop is not loaded again
			t.Errorf("%s: batch %s with %d left is gone", name, id, b.remaining())
		case ok && (g.consumed != b.consumed || g.expired != b.expired):
			t.Errorf("%s: batch %s consumed %d expired %d, live %d %d", name, id, g.consumed, g.expired, b.consumed, b.expired)
		}
	}
}
