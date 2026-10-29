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

// settled is a settle movement of the given net debit at seq.
func settled(seq, debit int64) movement {
	return movement{key: keyAt(seq), kind: "settle", request: uuid.NewV7(), debit: debit}
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
				applyAll(t, a, settled(int64(6+i), n))
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
	applyAll(t, a, settled(1, 50), creditOf(small), creditOf(large))
	if got := leftOf(a, []LiveBatchesRow{small, large}); !slices.Equal(got, []int64{0, 80}) || a.unfunded != 0 || a.funded() != 80 {
		t.Errorf("left %v, unfunded %d, funded %d; want [0 80], 0, 80", got, a.unfunded, a.funded())
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

// snapshot is what save stores of a, and the batches the next load finds: those that hold something, which include the
// ones not credited yet.
func snapshot(a *allocation) (AllocationStateRow, []LiveBatchesRow) {
	state := AllocationStateRow{AllocatedSeq: a.at.seq, AllocatedAt: a.at.at, AllocatedID: a.at.id, Unfunded: a.unfunded}
	var rows []LiveBatchesRow
	for _, b := range a.batches {
		if b.remaining() > 0 {
			rows = append(rows, LiveBatchesRow{
				ID: b.id, SiteID: b.site, Source: b.source, ExpiresAt: b.expiresAt, Amount: b.amount,
				Consumed: b.consumed, Expired: b.expired, FreezeTotal: b.freezeTotal, UnfreezeTotal: b.unfreezeTotal, Refunded: b.refunded, OpenedSeq: b.opened.seq, OpenedAt: b.opened.at, OpenedID: b.opened.id,
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
		out[i].FreezeTotal, out[i].UnfreezeTotal, out[i].Refunded = 0, 0, 0
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

// randomMovement includes charges into debt while credit is frozen, followed by release or refund.
func randomMovement(r *rand.Rand, live *allocation, rows []LiveBatchesRow, next *int, seq int64) movement {
	n := r.IntN(10)
	if n < 2 && *next < len(rows) {
		k := keyAt(seq)
		rows[*next].OpenedSeq, rows[*next].OpenedAt, rows[*next].OpenedID = k.seq, k.at, k.id
		live.batches[rows[*next].ID].opened = k
		*next++
		return creditOf(rows[*next-1])
	}
	if len(live.open) == 0 {
		return settled(seq, 1+r.Int64N(120))
	}
	return randomBatchMovement(r, live.open[r.IntN(len(live.open))], n, seq)
}

func randomBatchMovement(r *rand.Rand, b *batchState, n int, seq int64) movement {
	m := movement{key: keyAt(seq), request: b.id}
	switch {
	case n < 4 && b.available() > 0:
		m.kind, m.debit = "expire", 1+r.Int64N(b.available())
	case n < 6 && b.source == "paid" && b.available() > 0:
		m.kind, m.revision = "freeze", b.freezeTotal+1+r.Int64N(b.available())
	case n < 8 && b.frozen() > 0:
		m.kind, m.revision = "unfreeze", b.unfreezeTotal+1+r.Int64N(b.frozen())
	case n < 9 && b.frozen() > 0:
		m.kind, m.debit = "refund", 1+r.Int64N(b.frozen())
		m.revision = b.refunded + m.debit
	default:
		return settled(seq, 1+r.Int64N(120))
	}
	return m
}

// checkInvariants fails the test unless the allocation funds exactly the balance, no batch is over-spent, and debt is
// held only while no batch holds anything.
func checkInvariants(t testing.TB, live *allocation, balance int64, m movement) {
	t.Helper()
	if live.funded() != balance {
		t.Fatalf("after %+v the allocation funds %d of a balance of %d", m, live.funded(), balance)
	}
	for _, b := range live.batches {
		if b.consumed < 0 || b.expired < 0 || b.remaining() < b.frozen() || b.frozen() < 0 {
			t.Fatalf("batch %+v over-spent", *b)
		}
		if live.unfunded > 0 && b.opened.compare(live.at) <= 0 && b.available() > 0 {
			t.Fatalf("debt %d with available batch %+v", live.unfunded, *b)
		}
	}
}

// TestReplayProperty: over random credits, charges and write-offs, the allocation always funds exactly the
// balance, never over-spends or spends frozen credit, and holds debt only without available credit. A replay
// from nothing or from every possible stored position ends where the live replay did. Events are always replayed in their ledger sequence.
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
				checkFrozenEvents(t, live, log)
			}
			checkReplays(t, rows, log, live)
		})
	}
}

// checkReplays compares a full replay and a resume from every possible position with the live state.
func checkReplays(t *testing.T, rows []LiveBatchesRow, log []movement, live *allocation) {
	t.Helper()
	fresh := newAllocation(AllocationStateRow{}, pristine(rows))
	for i := 0; i <= len(log); i++ {
		resumed := newAllocation(snapshot(fresh))
		applyAll(t, resumed, log[i:]...)
		sameAllocation(t, fmt.Sprintf("resumed at %d", i), resumed, live)
		if i < len(log) {
			applyAll(t, fresh, log[i])
		}
	}
	sameAllocation(t, "from nothing", fresh, live)
}

func sameAllocation(t *testing.T, name string, got, live *allocation) {
	t.Helper()
	if got.unfunded != live.unfunded || got.at.compare(live.at) != 0 {
		t.Errorf("%s: unfunded %d at %v, live %d at %v", name, got.unfunded, got.at, live.unfunded, live.at)
	}
	for id, b := range live.batches {
		g, ok := got.batches[id]
		switch {
		case !ok && b.remaining() != 0: // a batch that holds nothing is not loaded again
			t.Errorf("%s: batch %s with %d left is gone", name, id, b.remaining())
		case ok && (g.consumed != b.consumed || g.expired != b.expired || g.freezeTotal != b.freezeTotal || g.unfreezeTotal != b.unfreezeTotal || g.refunded != b.refunded):
			t.Errorf("%s: batch %s got %+v, live %+v", name, id, *g, *b)
		}
	}
}

// TestReturnGivesBackTheCharge: a reversal is a return of what the charge took, not an allocation error (#277), and a
// correction's reverse and re-settle move only their difference, at the settle's place, so a batch that arrived in
// between is not used to refund the old charge.
func TestReturnGivesBackTheCharge(t *testing.T) {
	old := batchRow(1, 100, "gift", after(time.Hour))
	a := newAllocation(AllocationStateRow{}, []LiveBatchesRow{old})
	request := uuid.NewV7()
	if err := a.applyAll(uuid.UUID{}, []movement{
		creditOf(old),
		{key: keyAt(2), kind: "settle", request: request, debit: 80},
		{key: keyAt(3), kind: "reverse", request: request, debit: -10, revision: 1},
		{key: keyAt(4), kind: "settle", request: request, debit: 5, revision: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if got := leftOf(a, []LiveBatchesRow{old}); !slices.Equal(got, []int64{25}) || a.unfunded != 0 || a.funded() != 25 ||
		a.batches[old.ID].consumed != 75 {
		t.Fatalf("after the correction left %v, consumed %d, unfunded %d, funded %d; want [25], 75, 0, 25",
			got, a.batches[old.ID].consumed, a.unfunded, a.funded())
	}

	// Two batches are spent, then a credit that expires sooner arrives. A correction that changes nothing must not
	// refund the later batch and spend the new earlier one: the original allocation stays as it was.
	later := batchRow(2, 100, "gift", after(2*time.Hour))
	newer := batchRow(4, 100, "gift", after(30*time.Minute))
	b := newAllocation(AllocationStateRow{}, []LiveBatchesRow{old, later, newer})
	if err := b.applyAll(uuid.UUID{}, []movement{
		creditOf(old), creditOf(later),
		{key: keyAt(3), kind: "settle", request: request, debit: 200},
		creditOf(newer),
		{key: keyAt(5), kind: "reverse", request: request, debit: -10, revision: 1},
		{key: keyAt(6), kind: "settle", request: request, debit: 10, revision: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if got := leftOf(b, []LiveBatchesRow{old, later, newer}); !slices.Equal(got, []int64{0, 0, 100}) {
		t.Fatalf("after an equal correction left %v; want [0 0 100]: the new batch must stay unspent", got)
	}
}

// TestReturnDoesNotOpenAFutureCredit: a full replay loads batches whose credit is later in the history too. A refund
// from an earlier charge may only reopen batches that have already opened at the replay position; opening a future
// one and then crediting it again counts its money twice (funded 240 for a balance of 140).
func TestReturnDoesNotOpenAFutureCredit(t *testing.T) {
	old := batchRow(1, 100, "gift", nil)
	future := batchRow(4, 100, "gift", nil)
	request := uuid.NewV7()
	log := []movement{
		creditOf(old),
		{key: keyAt(2), kind: "settle", request: request, debit: 80},
		{key: keyAt(3), kind: "reverse", request: request, debit: -80, revision: 1},
		{key: keyAt(3), kind: "settle", request: request, debit: 60, revision: 1},
		creditOf(future),
	}
	a := newAllocation(AllocationStateRow{}, []LiveBatchesRow{old, future})
	applyAll(t, a, log...)
	if got := leftOf(a, []LiveBatchesRow{old, future}); !slices.Equal(got, []int64{40, 100}) || a.funded() != 140 {
		t.Fatalf("left %v, funded %d; want [40 100], 140", got, a.funded())
	}
	seen := map[*batchState]bool{}
	for _, b := range a.open {
		if seen[b] {
			t.Fatalf("open batches %v contain a duplicate", a.open)
		}
		seen[b] = true
	}
	if len(a.open) != 2 {
		t.Fatalf("open batches %v, want the two credited batches once each", a.open)
	}
	checkReplays(t, []LiveBatchesRow{old, future}, log, a)
}
