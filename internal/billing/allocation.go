package billing

import (
	"bytes"
	"cmp"
	"context"
	"errors"
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
//   - funded() equals the user's balance once every movement is replayed: the batches' leftovers less what charges took
//     beyond them;
//   - a batch is never over-spent: consumed + expired <= amount, so leftovers are never negative;
//   - unfunded > 0 only when no batch holds anything, since a credit pays it off before anything else.
//
// A movement that gives the user's balance back, other than a credit, is not allocated: nothing in the ledger makes one
// yet, and what the first will need is written in docs/domains/billing.md「额度批次」 (#277). The replay stops on it with
// an error, so an account is never given an allocation it does not have.

// orderKey is the place of a ledger transaction in the replay: its seq, which a transaction from before the sequence
// does not have and reads as 0, then its time and ID, which for those transactions is a key that never changes.
type orderKey struct {
	seq int64
	at  time.Time
	id  uuid.UUID
}

func (a orderKey) compare(b orderKey) int {
	return cmp.Or(cmp.Compare(a.seq, b.seq), a.at.Compare(b.at), bytes.Compare(a.id[:], b.id[:]))
}

// batchState is a credit batch as the replay sees it.
type batchState struct {
	id, site                  uuid.UUID
	source                    string
	expiresAt                 *time.Time
	amount, consumed, expired int64
	opened                    orderKey // its credit transaction: the batch holds nothing before it
	touched                   bool     // consumed or expired changed since it was loaded
}

func (b *batchState) remaining() int64 { return b.amount - b.consumed - b.expired }

// spendOrder is the order charges take from batches: the earliest expiry first, a batch that does not expire last; of
// the same expiry a gift before paid credit (so a paid batch stays refundable longest); then the older credit, which
// makes the order total and so the replay deterministic. A return gives back in the reverse order.
func spendOrder(a, b *batchState) int {
	return cmp.Or(compareExpiry(a.expiresAt, b.expiresAt), cmp.Compare(sourceRank(a.source), sourceRank(b.source)),
		a.opened.compare(b.opened))
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
	at      orderKey // the last movement replayed
	was     orderKey // where the stored state stood when it was loaded
	batches map[uuid.UUID]*batchState
	open    []*batchState // the credited batches that hold something, in spendOrder
	// unfunded is what charges took beyond every batch: the balance below zero.
	unfunded int64
	changed  bool // the place or the debt moved since it was loaded
}

// movement is one ledger transaction's effect on the user's balance: debit is positive when it takes from the user,
// 0 when the transaction did not touch it.
type movement struct {
	key     orderKey
	kind    string
	request uuid.UUID
	debit   int64
}

func newAllocation(state AllocationStateRow, loaded []LiveBatchesRow) *allocation {
	at := orderKey{seq: state.AllocatedSeq, at: state.AllocatedAt, id: state.AllocatedID}
	a := &allocation{at: at, was: at, unfunded: state.Unfunded, batches: make(map[uuid.UUID]*batchState, len(loaded))}
	for _, r := range loaded {
		b := &batchState{
			id: r.ID, site: r.SiteID, source: r.Source, expiresAt: r.ExpiresAt, amount: r.Amount, consumed: r.Consumed,
			expired: r.Expired, opened: orderKey{seq: r.OpenedSeq, at: r.OpenedAt, id: r.OpenedID},
		}
		a.batches[b.id] = b
		if b.opened.compare(at) <= 0 {
			a.open = append(a.open, b)
		}
	}
	slices.SortFunc(a.open, spendOrder)
	return a
}

// funded is what the batches hold, less the debt: the balance, if every movement is replayed.
func (a *allocation) funded() int64 {
	total := -a.unfunded
	for _, b := range a.open {
		total += b.remaining()
	}
	return total
}

// apply replays one movement. Movements come in order, once each.
func (a *allocation) apply(m movement) error {
	if m.key.compare(a.at) <= 0 {
		return fmt.Errorf("billing: movement %v replayed after %v", m.key, a.at)
	}
	var err error
	switch {
	case m.kind == "credit":
		err = a.credit(m)
	case m.kind == "expire":
		err = a.writeOff(m)
	case m.debit > 0:
		a.spend(m.debit)
	case m.debit < 0:
		err = fmt.Errorf("billing: transaction %s (%s) gives %d back to the balance, which is not allocated yet", m.key.id, m.kind, -m.debit)
	}
	if err != nil {
		return err
	}
	a.at, a.changed = m.key, true
	return nil
}

// credit opens the batch the transaction made. What charges took beyond every batch before it is paid off from it
// first, since the balance counts the debt against the credit; a batch the debt eats whole holds nothing.
func (a *allocation) credit(m movement) error {
	b, ok := a.batches[m.request]
	if !ok || b.opened.id != m.key.id || m.debit != -b.amount {
		return fmt.Errorf("billing: credit transaction %s does not match a batch %s", m.key.id, m.request)
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
		return fmt.Errorf("billing: expiry transaction %s of %d does not fit batch %s", m.key.id, m.debit, m.request)
	}
	b.expired += m.debit
	b.touched = true
	a.dropSpent()
	return nil
}

// spend takes n from the batches in spendOrder, and what none holds is debt. A batch past its expiry that nobody has
// written off yet is still spent from: the write-off takes only what is left when it runs (docs/domains/billing.md
// 「额度批次」「到期」).
func (a *allocation) spend(n int64) {
	for _, b := range a.open {
		take := min(b.remaining(), n)
		b.consumed += take
		b.touched = b.touched || take > 0
		n -= take
	}
	a.dropSpent()
	a.unfunded += n
}

func (a *allocation) dropSpent() {
	a.open = slices.DeleteFunc(a.open, func(b *batchState) bool { return b.remaining() == 0 })
}

// movementPage is how many transactions one read of the ledger takes.
const movementPage = 5000

// loadAllocation reads the stored state of an account: where the replay stood, then the batches. The state comes
// first, so that a replay that another transaction moved between the two reads is dropped by save's check of the place
// (the batches read later are never older than the place read earlier). A caller that holds the account's balance row
// lock reads the latest state, and no other transaction can move it.
func loadAllocation(ctx context.Context, tx pg.Tx, account uuid.UUID) (*allocation, error) {
	state, err := queries.AllocationState(ctx, tx, account)
	if err != nil {
		return nil, fmt.Errorf("billing: read the allocation of %s: %w", account, err)
	}
	loaded, err := queries.LiveBatches(ctx, tx, account)
	if err != nil {
		return nil, fmt.Errorf("billing: read the batches of %s: %w", account, err)
	}
	return newAllocation(state, loaded), nil
}

// catchUpPage replays the next page of at most size transactions after the last one replayed, and reports whether
// the page was full, so that there may be more. The transactions from before the sequence come first; the page that
// ends them goes on into the numbered ones. A page is bounded by the index, not by the history.
func (a *allocation) catchUpPage(ctx context.Context, tx pg.Tx, account uuid.UUID, size int32) (full bool, err error) {
	if a.at.seq == 0 {
		rows, err := queries.LegacyMovements(ctx, tx, LegacyMovementsParams{AccountID: account, AfterAt: a.at.at, AfterID: a.at.id, Max: size})
		if err != nil {
			return false, fmt.Errorf("billing: read the movements of %s: %w", account, err)
		}
		for _, r := range rows {
			if err := a.applyRow(account, r.Seq, r.CreatedAt, r.ID, r.Kind, r.RequestID, r.Debit); err != nil {
				return false, err
			}
		}
		if len(rows) == int(size) {
			return true, nil
		}
	}
	rows, err := queries.Movements(ctx, tx, MovementsParams{AccountID: account, AfterSeq: a.at.seq, Max: size})
	if err != nil {
		return false, fmt.Errorf("billing: read the movements of %s: %w", account, err)
	}
	for _, r := range rows {
		if err := a.applyRow(account, r.Seq, r.CreatedAt, r.ID, r.Kind, r.RequestID, r.Debit); err != nil {
			return false, err
		}
	}
	return len(rows) == int(size), nil
}

func (a *allocation) applyRow(account uuid.UUID, seq int64, at time.Time, id uuid.UUID, kind string, request uuid.UUID, debit int64) error {
	m := movement{key: orderKey{seq: seq, at: at, id: id}, kind: kind, request: request, debit: debit}
	if err := a.apply(m); err != nil {
		return fmt.Errorf("billing: replay %s: %w", account, err)
	}
	return nil
}

// catchUp replays every transaction posted since the last one replayed.
func (a *allocation) catchUp(ctx context.Context, tx pg.Tx, account uuid.UUID, size int32) error {
	for {
		full, err := a.catchUpPage(ctx, tx, account, size)
		if err != nil || !full {
			return err
		}
	}
}

// errStale is a replay dropped because another transaction moved the stored allocation while it ran.
var errStale = errors.New("billing: the allocation moved while it was replayed")

// save stores what the replay changed, if the stored allocation still stands where the replay began.
func (a *allocation) save(ctx context.Context, tx pg.Tx, account uuid.UUID) error {
	if !a.changed {
		return nil
	}
	n, err := queries.SaveAllocation(ctx, tx, SaveAllocationParams{
		AccountID: account, AllocatedSeq: a.at.seq, AllocatedAt: a.at.at, AllocatedID: a.at.id, Unfunded: a.unfunded,
		WasSeq: a.was.seq, WasAt: a.was.at, WasID: a.was.id,
	})
	if err != nil {
		return fmt.Errorf("billing: save the allocation of %s: %w", account, err)
	}
	if n == 0 {
		return errStale
	}
	var batches SaveBatchesParams
	for _, b := range a.batches {
		if b.touched {
			batches.Ids, batches.Consumed, batches.Expired = append(batches.Ids, b.id), append(batches.Consumed, b.consumed), append(batches.Expired, b.expired)
		}
	}
	if len(batches.Ids) == 0 {
		return nil
	}
	if err := queries.SaveBatches(ctx, tx, batches); err != nil {
		return fmt.Errorf("billing: save the batches of %s: %w", account, err)
	}
	return nil
}
