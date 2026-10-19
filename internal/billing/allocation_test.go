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

// keyAt is the place of the transaction numbered seq.
func keyAt(seq int64) orderKey {
	return orderKey{seq: seq, at: epoch.Add(time.Duration(seq) * time.Second), id: uuid.NewV7()}
}

// stateAt is an allocation stored as replayed up to the transaction numbered seq, whatever its ID.
func stateAt(seq int64) AllocationStateRow {
	return AllocationStateRow{AllocatedSeq: seq, AllocatedAt: epoch.Add(time.Duration(seq) * time.Second), AllocatedID: uuid.UUID(slices.Repeat([]byte{0xff}, 16))}
}

// batchRow is a batch as the stored state has it, opened by the credit transaction numbered seq.
func batchRow(seq, amount int64, source string, expiresAt *time.Time) LiveBatchesRow {
	k := keyAt(seq)
	return LiveBatchesRow{ID: uuid.NewV7(), Source: source, ExpiresAt: expiresAt, Amount: amount, OpenedSeq: k.seq, OpenedAt: k.at, OpenedID: k.id}
}

func after(d time.Duration) *time.Time { t := epoch.Add(d); return &t }

func creditOf(r LiveBatchesRow) movement {
	return movement{key: orderKey{seq: r.OpenedSeq, at: r.OpenedAt, id: r.OpenedID}, kind: "credit", request: r.ID, debit: -r.Amount}
}

// settled is a movement of the given kind and net debit at seq.
func settled(seq int64, kind string, debit int64) movement {
	return movement{key: keyAt(seq), kind: kind, request: uuid.NewV7(), debit: debit}
}

// leftOf is what each batch has left, by position in rows.
func leftOf(a *allocation, rows []LiveBatchesRow) []int64 {
	left := make([]int64, len(rows))
	for i, r := range rows {
		left[i] = a.batches[r.ID].remaining()
	}
	return left
}

func applyAll(t *testing.T, a *allocation, ms ...movement) {
	t.Helper()
	for _, m := range ms {
		if err := a.apply(m); err != nil {
			t.Fatalf("movement %+v: %v", m, err)
		}
	}
}

// TestSpendOrder: a charge takes the batch that expires first, a batch that does not expire last; of the same expiry a
// gift before paid credit, then the older; what no batch holds is debt that the next credit pays off first.
func TestSpendOrder(t *testing.T) {
	rows := []LiveBatchesRow{
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
			a := newAllocation(stateAt(5), rows)
			for i, n := range tc.spend {
				applyAll(t, a, settled(int64(6+i), "settle", n))
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
	a := newAllocation(AllocationStateRow{}, []LiveBatchesRow{small, large})
	applyAll(t, a, settled(1, "settle", 50), creditOf(small), creditOf(large))
	if got := leftOf(a, []LiveBatchesRow{small, large}); !slices.Equal(got, []int64{0, 80}) || a.unfunded != 0 || a.funded() != 80 {
		t.Errorf("left %v, unfunded %d, funded %d; want [0 80], 0, 80", got, a.unfunded, a.funded())
	}
}

// TestGiveBack: a charge given back pays the debt first, then goes back to the batches it was taken from, the last
// taken first, so each keeps its own expiry: a batch that has expired holds the part given back until it is written
// off again, and a reversal followed by the settlement that replaces it nets out.
func TestGiveBack(t *testing.T) {
	gift, lasting := batchRow(1, 100, "gift", after(time.Hour)), batchRow(2, 100, "gift", nil)
	rows := []LiveBatchesRow{gift, lasting}
	for _, tc := range []struct {
		name  string
		moves []movement
		left  []int64 // by row
		debt  int64
	}{
		{"debt first, then the batch", []movement{settled(3, "settle", 230), settled(4, "reverse", -50)}, []int64{0, 20}, 0},
		{"the batch that never expires, which was taken last, first", []movement{settled(3, "settle", 130), settled(4, "reverse", -20)}, []int64{0, 90}, 0},
		{"then the batch before it", []movement{settled(3, "settle", 130), settled(4, "reverse", -50)}, []int64{20, 100}, 0},
		{"a reversal and the settlement that replaces it", []movement{settled(3, "settle", 60), settled(4, "reverse", -60), settled(5, "settle", 50)}, []int64{50, 100}, 0},
		{"a batch written off holds what is given back until it is written off again", []movement{
			settled(3, "settle", 40), {key: keyAt(4), kind: "expire", request: gift.ID, debit: 60}, settled(5, "reverse", -10),
		}, []int64{10, 100}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAllocation(stateAt(2), rows)
			applyAll(t, a, tc.moves...)
			if got := leftOf(a, rows); !slices.Equal(got, tc.left) || a.unfunded != tc.debt {
				t.Errorf("left %v, unfunded %d; want %v, %d", got, a.unfunded, tc.left, tc.debt)
			}
		})
	}
}

// TestGiveBackBeyondWhatWasCharged: a return larger than all that was charged is a ledger that cannot be, and stops
// the replay.
func TestGiveBackBeyondWhatWasCharged(t *testing.T) {
	gift := batchRow(1, 100, "gift", nil)
	a := newAllocation(stateAt(1), []LiveBatchesRow{gift})
	applyAll(t, a, settled(2, "settle", 30))
	if err := a.apply(settled(3, "reverse", -31)); err == nil {
		t.Error("a return of 31 after a charge of 30 was replayed")
	}
}

// TestApplyRefusesWhatTheLedgerCannotHold: a replay that meets a movement out of order, an expiry larger than what its
// batch holds, or a credit that is not its batch stops instead of making up an allocation.
func TestApplyRefusesWhatTheLedgerCannotHold(t *testing.T) {
	gift := batchRow(1, 100, "gift", after(time.Hour))
	for name, ms := range map[string][]movement{
		"out of order":         {creditOf(gift), creditOf(gift)},
		"a credit of another":  {{key: keyAt(1), kind: "credit", request: gift.ID, debit: -99}},
		"a credit unknown":     {{key: keyAt(1), kind: "credit", request: uuid.NewV7(), debit: -100}},
		"an expiry too large":  {creditOf(gift), {key: keyAt(2), kind: "expire", request: gift.ID, debit: 101}},
		"an expiry of nothing": {creditOf(gift), {key: keyAt(2), kind: "expire", request: gift.ID, debit: 0}},
	} {
		t.Run(name, func(t *testing.T) {
			a := newAllocation(AllocationStateRow{}, []LiveBatchesRow{gift})
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

// TestLegacyTransactionsComeFirst: a transaction from before the sequence reads as seq 0 and is ordered by its time and
// ID, ahead of every numbered one.
func TestLegacyTransactionsComeFirst(t *testing.T) {
	older, newer, numbered := orderKey{at: epoch, id: uuid.NewV7()}, orderKey{at: epoch.Add(time.Second)}, orderKey{seq: 1, at: epoch.Add(-time.Hour)}
	if older.compare(newer) >= 0 || newer.compare(numbered) >= 0 || older.compare(older) != 0 {
		t.Error("the replay order is not (seq, time, id)")
	}
}

// snapshot is what save stores of a, and the batches the next load finds: those that hold something, or were spent
// from, or have not been credited yet.
func snapshot(a *allocation) (AllocationStateRow, []LiveBatchesRow) {
	state := AllocationStateRow{AllocatedSeq: a.at.seq, AllocatedAt: a.at.at, AllocatedID: a.at.id, Unfunded: a.unfunded}
	var rows []LiveBatchesRow
	for _, b := range a.batches {
		if b.consumed > 0 || b.remaining() > 0 {
			rows = append(rows, LiveBatchesRow{
				ID: b.id, SiteID: b.site, Source: b.source, ExpiresAt: b.expiresAt, Amount: b.amount,
				Consumed: b.consumed, Expired: b.expired, OpenedSeq: b.opened.seq, OpenedAt: b.opened.at, OpenedID: b.opened.id,
			})
		}
	}
	return state, rows
}

// pristine is the batches as the credit transactions made them, before any replay.
func pristine(rows []LiveBatchesRow) []LiveBatchesRow {
	out := slices.Clone(rows)
	for i := range out {
		out[i].Consumed, out[i].Expired = 0, 0
	}
	return out
}

// randomBatches are the batches a property run credits, not yet credited: their places are set when the credit is
// posted.
func randomBatches(r *rand.Rand) []LiveBatchesRow {
	rows := make([]LiveBatchesRow, 1+r.IntN(5))
	for i := range rows {
		var expires *time.Time
		if r.IntN(3) > 0 {
			expires = after(time.Duration(r.IntN(4)) * time.Hour)
		}
		rows[i] = batchRow(1<<40, 1+r.Int64N(200), []string{"gift", "paid"}[r.IntN(2)], expires)
	}
	return rows
}

// charged is all that a return may give back: the debt and what the batches were charged.
func charged(a *allocation) int64 {
	total := a.unfunded
	for _, b := range a.live {
		total += b.consumed
	}
	return total
}

// randomMovement is the next movement of a run: a credit of the next batch, a write-off of part of an open batch, a
// charge given back, or a charge.
func randomMovement(r *rand.Rand, live *allocation, rows []LiveBatchesRow, next *int, seq int64) movement {
	switch n := r.IntN(10); {
	case n < 2 && *next < len(rows):
		k := keyAt(seq)
		rows[*next].OpenedSeq, rows[*next].OpenedAt, rows[*next].OpenedID = k.seq, k.at, k.id
		live.batches[rows[*next].ID].opened = k
		*next++
		return creditOf(rows[*next-1])
	case n < 4 && len(live.open) > 0:
		b := live.open[r.IntN(len(live.open))]
		return movement{key: keyAt(seq), kind: "expire", request: b.id, debit: 1 + r.Int64N(b.remaining())}
	case n < 5 && charged(live) > 0:
		return settled(seq, "reverse", -(1 + r.Int64N(min(80, charged(live)))))
	default:
		return settled(seq, "settle", 1+r.Int64N(120))
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
	if live.unfunded > 0 && len(live.open) > 0 {
		t.Fatalf("debt %d with %d batches open", live.unfunded, len(live.open))
	}
}

// TestReplayProperty: over random credits, charges, write-offs and returns, the allocation always funds exactly the
// balance, never over-spends a batch, and holds debt only while no batch holds anything; and replaying from nothing,
// or resuming from what was stored at several points, ends where the live replay did.
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

// checkReplays replays log from nothing, and again with the state stored and loaded anew at three random points; each
// must end where live did.
func checkReplays(t *testing.T, r *rand.Rand, rows []LiveBatchesRow, log []movement, live *allocation) {
	t.Helper()
	fresh := newAllocation(AllocationStateRow{}, pristine(rows))
	resumed := newAllocation(AllocationStateRow{}, pristine(rows))
	stops := []int{r.IntN(len(log)), r.IntN(len(log)), r.IntN(len(log))}
	for i, m := range log {
		if err := fresh.apply(m); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(stops, i) {
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
	if got.unfunded != live.unfunded || got.at.compare(live.at) != 0 {
		t.Errorf("%s: unfunded %d at %v, live %d at %v", name, got.unfunded, got.at, live.unfunded, live.at)
	}
	for id, b := range live.batches {
		g, ok := got.batches[id]
		switch {
		case !ok && (b.remaining() != 0 || b.consumed != 0): // a batch that holds nothing and was never spent from is not loaded again
			t.Errorf("%s: batch %s with %d left and %d consumed is gone", name, id, b.remaining(), b.consumed)
		case ok && (g.consumed != b.consumed || g.expired != b.expired):
			t.Errorf("%s: batch %s consumed %d expired %d, live %d %d", name, id, g.consumed, g.expired, b.consumed, b.expired)
		}
	}
}
