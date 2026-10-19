package billing

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// The allocation says how much of each credit batch is left (docs/domains/billing.md「额度批次」). It is not written by
// a charge: it is a replay of the account's user-balance movements, in the order they were posted, onto its batches.
// Replaying the whole ledger from nothing and resuming from the stored state reach the same result, so the stored state
// is a cache of a pure function of the ledger.
//
// Invariants, which allocation_test.go and expire_test.go check after every step:
//   - funded() equals the user's balance once every movement is replayed: the batches' leftovers plus the credit that
//     belongs to no batch, less what charges took beyond them;
//   - a batch is never over-spent: consumed + expired <= amount, so leftovers are never negative;
//   - unfunded > 0 only when no batch holds anything, since a credit pays it off before anything else.

// batchState is a credit batch as the replay sees it.
type batchState struct {
	id, site                  uuid.UUID
	source                    string
	expiresAt                 *time.Time
	amount, consumed, expired int64
	opened                    int64 // the sequence number of its credit transaction: it holds nothing before that
	touched                   bool  // consumed or expired changed since it was loaded
}

func (b *batchState) remaining() int64 { return b.amount - b.consumed - b.expired }

// spendOrder is the order charges take from batches: the earliest expiry first, a batch that does not expire last; of
// the same expiry a gift before paid credit (so a paid batch stays refundable longest); then the older credit, which
// makes the order total and so the replay deterministic.
func spendOrder(a, b *batchState) int {
	return cmp.Or(compareExpiry(a.expiresAt, b.expiresAt), cmp.Compare(sourceRank(a.source), sourceRank(b.source)),
		cmp.Compare(a.opened, b.opened))
}

func compareExpiry(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	}
	return a.Compare(*b)
}

// sourceRank puts gifts before every kind of paid credit; the payments design adds its sources here.
func sourceRank(source string) int {
	if source == "gift" {
		return 0
	}
	return 1
}

// allocation is the replay's state for one account.
type allocation struct {
	seq     int64 // the last movement replayed
	batches map[uuid.UUID]*batchState
	open    []*batchState // the batches that hold something and have been credited, in spendOrder
	// unfunded is what charges took beyond every batch: the balance below zero. unbatched is credit that belongs to no
	// batch, such as a charge given back after its batch was written off; it is spent after every batch.
	unfunded, unbatched int64
	changed             bool // seq, unfunded or unbatched moved since it was loaded
}

// movement is one ledger transaction's effect on the user's balance: debit is positive when it takes from the user.
type movement struct {
	seq     int64
	kind    string
	request uuid.UUID
	debit   int64
}

func newAllocation(state AllocationStateRow, loaded []UnspentBatchesRow) *allocation {
	a := &allocation{
		seq: state.AllocatedSeq, unfunded: state.Unfunded, unbatched: state.Unbatched,
		batches: make(map[uuid.UUID]*batchState, len(loaded)),
	}
	for _, r := range loaded {
		b := &batchState{
			id: r.ID, site: r.SiteID, source: r.Source, expiresAt: r.ExpiresAt, amount: r.Amount, consumed: r.Consumed,
			expired: r.Expired, opened: r.Seq,
		}
		a.batches[b.id] = b
		if b.opened <= a.seq {
			a.open = append(a.open, b)
		}
	}
	slices.SortFunc(a.open, spendOrder)
	return a
}

// funded is what the batches and the credit outside them add up to, less the debt: the balance, if every movement is
// replayed.
func (a *allocation) funded() int64 {
	total := a.unbatched - a.unfunded
	for _, b := range a.open {
		total += b.remaining()
	}
	return total
}

// apply replays one movement. Movements come in sequence order, once each.
func (a *allocation) apply(m movement) error {
	if m.seq <= a.seq {
		return fmt.Errorf("billing: movement %d replayed after %d", m.seq, a.seq)
	}
	var err error
	switch {
	case m.kind == "credit":
		err = a.credit(m)
	case m.kind == "expire":
		err = a.writeOff(m)
	case m.debit > 0:
		a.spend(m.debit)
	default:
		a.giveBack(-m.debit)
	}
	if err != nil {
		return err
	}
	a.seq, a.changed = m.seq, true
	return nil
}

// credit opens the batch the transaction made. What charges took beyond every batch before it is paid off from it
// first, since the balance counts the debt against the credit; a batch the debt eats whole holds nothing.
func (a *allocation) credit(m movement) error {
	b, ok := a.batches[m.request]
	if !ok || b.opened != m.seq || m.debit != -b.amount {
		return fmt.Errorf("billing: credit transaction %d does not match a batch %s", m.seq, m.request)
	}
	paid := min(a.unfunded, b.amount)
	a.unfunded -= paid
	b.consumed += paid
	b.touched = b.touched || paid > 0
	if b.remaining() > 0 {
		a.open = append(a.open, b)
		slices.SortFunc(a.open, spendOrder)
	}
	return nil
}

// writeOff takes an expiry from the batch it names.
func (a *allocation) writeOff(m movement) error {
	b, ok := a.batches[m.request]
	if !ok || m.debit <= 0 || m.debit > b.remaining() {
		return fmt.Errorf("billing: expiry transaction %d of %d does not fit batch %s", m.seq, m.debit, m.request)
	}
	b.expired += m.debit
	b.touched = true
	a.dropSpent()
	return nil
}

// spend takes n from the batches in spendOrder, then from the credit outside them, and what neither holds is debt.
// A batch past its expiry that nobody has written off yet is still spent from: the write-off takes only what is left
// when it runs (docs/domains/billing.md「额度批次」「到期」).
func (a *allocation) spend(n int64) {
	for _, b := range a.open {
		take := min(b.remaining(), n)
		b.consumed += take
		b.touched = b.touched || take > 0
		n -= take
	}
	a.dropSpent()
	take := min(a.unbatched, n)
	a.unbatched -= take
	a.unfunded += n - take
}

// giveBack takes n back from the debt, and what the debt does not hold becomes credit outside the batches: the batch
// it came from may have been written off, and a written-off batch is never revived.
func (a *allocation) giveBack(n int64) {
	paid := min(a.unfunded, n)
	a.unfunded -= paid
	a.unbatched += n - paid
}

func (a *allocation) dropSpent() {
	a.open = slices.DeleteFunc(a.open, func(b *batchState) bool { return b.remaining() == 0 })
}

// movementPage is how many movements one read takes.
const movementPage = 5000

// loadAllocation reads the stored state of an account. The caller holds the account's balance row lock, which keeps
// every writer of its user balance out until the transaction ends and makes the state read here the latest: a
// transaction that moves the balance takes its sequence number under that lock, so none can appear below seq later.
func loadAllocation(ctx context.Context, tx pg.Tx, account uuid.UUID) (*allocation, error) {
	state, err := queries.AllocationState(ctx, tx, account)
	if err != nil {
		return nil, fmt.Errorf("billing: read the allocation of %s: %w", account, err)
	}
	loaded, err := queries.UnspentBatches(ctx, tx, account)
	if err != nil {
		return nil, fmt.Errorf("billing: read the batches of %s: %w", account, err)
	}
	return newAllocation(state, loaded), nil
}

// catchUp replays every movement posted since the last one replayed.
func (a *allocation) catchUp(ctx context.Context, tx pg.Tx, account uuid.UUID) error {
	for {
		rows, err := queries.Movements(ctx, tx, MovementsParams{AccountID: account, After: a.seq, Max: movementPage})
		if err != nil {
			return fmt.Errorf("billing: read the movements of %s: %w", account, err)
		}
		for _, r := range rows {
			if err := a.apply(movement{seq: r.Seq, kind: r.Kind, request: r.RequestID, debit: r.Debit}); err != nil {
				return fmt.Errorf("billing: replay %s: %w", account, err)
			}
		}
		if len(rows) < movementPage {
			return nil
		}
	}
}

// save stores what the replay changed.
func (a *allocation) save(ctx context.Context, tx pg.Tx, account uuid.UUID) error {
	var batches SaveBatchesParams
	for _, b := range a.batches {
		if b.touched {
			batches.Ids, batches.Consumed, batches.Expired = append(batches.Ids, b.id), append(batches.Consumed, b.consumed), append(batches.Expired, b.expired)
		}
	}
	if len(batches.Ids) > 0 {
		if err := queries.SaveBatches(ctx, tx, batches); err != nil {
			return fmt.Errorf("billing: save the batches of %s: %w", account, err)
		}
	}
	if !a.changed {
		return nil
	}
	err := queries.SaveAllocation(ctx, tx, SaveAllocationParams{
		AccountID: account, AllocatedSeq: a.seq, Unfunded: a.unfunded, Unbatched: a.unbatched,
	})
	if err != nil {
		return fmt.Errorf("billing: save the allocation of %s: %w", account, err)
	}
	return nil
}
