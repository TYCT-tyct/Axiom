package billing

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
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
//   - a batch is never over-spent: consumed + expired + refunded + frozen <= amount, so leftovers are never negative;
//   - unfunded > 0 only when no batch holds unfrozen credit, since a credit pays it off before anything else.
//
// A movement that gives the user's balance back, other than a credit, is returned to the batches that took it: the debt
// first, then reverse spend order, lowering an expired batch's write-off when its consumed credit is not enough. A
// correction's reverse and re-settle are netted at the settle's place, so a return cannot move credit to another batch.

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
	id, site                             uuid.UUID
	source                               string
	expiresAt                            *time.Time
	amount, consumed, expired            int64
	freezeTotal, unfreezeTotal, refunded int64
	chargedBack                          int64
	opened                               orderKey // its credit transaction: the batch holds nothing before it
	touched                              bool     // replay state changed since it was loaded
}

func (b *batchState) remaining() int64 {
	return b.amount - b.consumed - b.expired - b.refunded - b.chargedBack
}

func (b *batchState) frozen() int64 { return b.freezeTotal - b.unfreezeTotal - b.refunded }

func (b *batchState) available() int64 { return b.remaining() - b.frozen() }

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
	// unfunded is what charges took beyond unfrozen credit; frozen credit can still keep the balance positive.
	unfunded int64
	changed  bool // the place or the debt moved since it was loaded
}

// movement is one ledger transaction's effect on the user's balance: debit is positive when it takes from the user,
// 0 when the transaction did not touch it.
type movement struct {
	key      orderKey
	kind     string
	request  uuid.UUID
	debit    int64
	revision int64
}

// placeOf is where a stored allocation stands: the last movement it replayed.
func placeOf(state AllocationStateRow) orderKey {
	return orderKey{seq: state.AllocatedSeq, at: state.AllocatedAt, id: state.AllocatedID}
}

func newAllocation(state AllocationStateRow, loaded []LiveBatchesRow) *allocation {
	at := placeOf(state)
	a := &allocation{at: at, was: at, unfunded: state.Unfunded, batches: make(map[uuid.UUID]*batchState, len(loaded))}
	for _, r := range loaded {
		b := &batchState{
			id: r.ID, site: r.SiteID, source: r.Source, expiresAt: r.ExpiresAt, amount: r.Amount, consumed: r.Consumed,
			expired: r.Expired, opened: orderKey{seq: r.OpenedSeq, at: r.OpenedAt, id: r.OpenedID},
			freezeTotal: r.FreezeTotal, unfreezeTotal: r.UnfreezeTotal, refunded: r.Refunded, chargedBack: r.ChargedBack,
		}
		a.batches[b.id] = b
		if b.opened.compare(at) <= 0 {
			a.open = append(a.open, b)
		}
	}
	slices.SortFunc(a.open, spendOrder)
	return a
}

// chargeback takes or gives back credit of the named batch without touching a freeze. A chargeback's debit is
// positive and a reversal's is negative, so the cumulative column moves by the movement's debit either way. The batch
// is the one the payment credited: spending the debit from other batches would leave the charged batch refundable and
// take the loss twice.
func (a *allocation) chargeback(m movement) error {
	b := a.batches[m.request]
	if b == nil {
		return fmt.Errorf("chargeback %s is not an open batch", m.request)
	}
	b.chargedBack += m.debit
	// Consumed credit does not protect a payment from a chargeback: the user may have spent it, and the debt then takes
	// the balance negative. Only a refund and a chargeback together cannot exceed what the payment credited.
	if b.chargedBack < 0 || b.refunded+b.chargedBack > b.amount {
		return fmt.Errorf("chargeback leaves batch %s with %d charged back of %d", b.id, b.chargedBack, b.amount)
	}
	b.touched = true
	a.reopen()
	return nil
}

// funded is what the batches hold, less the debt: the balance, if every movement is replayed.
func (a *allocation) funded() int64 {
	total := -a.unfunded
	for _, b := range a.open {
		total += b.remaining()
	}
	return total
}

// restoreBatch adds a batch that LiveBatches left out, so a chargeback can still name it. Its opening key is at or
// before the allocation place; keeping it in the spend order makes the balance replay see the same batch.
func (a *allocation) restoreBatch(b *batchState) {
	a.batches[b.id] = b
	if b.opened.compare(a.at) <= 0 {
		a.open = append(a.open, b)
		slices.SortFunc(a.open, spendOrder)
	}
}

func (a *allocation) frozen() int64 {
	var total int64
	for _, b := range a.open {
		total += b.frozen()
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
	case m.kind == "freeze" || m.kind == "unfreeze" || m.kind == "refund":
		err = a.refund(m)
	case m.kind == "chargeback" || m.kind == "chargeback_reverse":
		err = a.chargeback(m)
	case m.debit > 0:
		a.spend(m.debit)
	case m.debit < 0:
		err = a.unspend(-m.debit)
	}
	if err != nil {
		return err
	}
	a.at, a.changed = m.key, true
	return nil
}

// applyAll replays a page, netting a correction's reverse and re-settle into one movement at the settle's place. The
// two are posted in one transaction and so are adjacent; applying them apart would give credit back and spend it
// again, which can move the refund to a batch that expires at another time (docs/domains/billing.md「额度批次」).
func (a *allocation) applyAll(account uuid.UUID, moves []movement) error {
	for i := 0; i < len(moves); i++ {
		m := moves[i]
		if m.kind == "reverse" && m.revision != 0 && i+1 < len(moves) {
			next := moves[i+1]
			if next.kind == "settle" && next.request == m.request && next.revision == m.revision {
				if err := a.applyNet(m, next); err != nil {
					return fmt.Errorf("billing: replay %s: %w", account, err)
				}
				i++
				continue
			}
		}
		if err := a.apply(m); err != nil {
			return fmt.Errorf("billing: replay %s: %w", account, err)
		}
	}
	return nil
}

// applyNet applies what a reverse and the settle that follows it did to the balance: only the difference moves, at the
// settle's place. A refund restores the batches in reverse spend order, which never gives a later-expiring batch back
// while an earlier-expiring one is still spent.
func (a *allocation) applyNet(reverse, settle movement) error {
	if settle.key.compare(a.at) <= 0 || reverse.key.compare(a.at) <= 0 {
		return fmt.Errorf("movement %v replayed after %v", settle.key, a.at)
	}
	net := settle.debit + reverse.debit // reverse is negative
	a.at, a.changed = settle.key, true
	switch {
	case net > 0:
		a.spend(net)
	case net < 0:
		if err := a.unspend(-net); err != nil {
			return err
		}
	}
	return nil
}

// unspend gives n back to the batches a charge took, in reverse spend order, and pays the debt first. An expired
// batch's write-off is lowered when its consumed credit is not enough, so the refund cannot outlive the expiry the
// next run will apply again.
func (a *allocation) unspend(n int64) error {
	if n <= 0 {
		return nil
	}
	if paid := min(a.unfunded, n); paid > 0 {
		a.unfunded -= paid
		n -= paid
	}
	if n == 0 {
		return nil
	}
	returned := n
	if n = giveBack(a.refundable(), n); n > 0 {
		return fmt.Errorf("a return of %d has %d that no credit batch took", returned, n)
	}
	a.reopen()
	return nil
}

// refundable are the batches a refund may give back to, in reverse spend order. A batch whose credit is later in the
// replay has not opened yet, and must not receive an earlier charge's refund: credit() would then add the same batch
// to open again, counting its money twice.
func (a *allocation) refundable() []*batchState {
	candidates := make([]*batchState, 0, len(a.batches))
	for _, b := range a.batches {
		if b.opened.compare(a.at) <= 0 && (b.consumed > 0 || b.expired > 0) {
			candidates = append(candidates, b)
		}
	}
	slices.SortFunc(candidates, func(x, y *batchState) int { return spendOrder(y, x) })
	return candidates
}

// giveBack returns as much of n as the batches hold, spent credit first and written-off credit after it, and reports
// what none of them took.
func giveBack(candidates []*batchState, n int64) int64 {
	for _, b := range candidates {
		give := min(b.consumed, n)
		b.consumed -= give
		b.touched = b.touched || give > 0
		n -= give
		if n == 0 {
			return 0
		}
	}
	for _, b := range candidates {
		give := min(b.expired, n)
		b.expired -= give
		b.touched = b.touched || give > 0
		n -= give
		if n == 0 {
			return 0
		}
	}
	return n
}

// reopen puts every batch that still holds credit back into spend order, including one a refund replenished.
func (a *allocation) reopen() {
	for _, b := range a.batches {
		if b.opened.compare(a.at) <= 0 && b.remaining() > 0 && !slices.Contains(a.open, b) {
			a.open = append(a.open, b)
		}
	}
	slices.SortFunc(a.open, spendOrder)
}

// credit opens the batch the transaction made. What charges took beyond every batch before it is paid off from it
// first, since the balance counts the debt against the credit; a batch the debt eats whole holds nothing.
func (a *allocation) credit(m movement) error {
	b, ok := a.batches[m.request]
	if !ok || b.opened.id != m.key.id || m.debit != -b.amount {
		return fmt.Errorf("billing: credit transaction %s does not match a batch %s", m.key.id, m.request)
	}
	a.payDebt(b, b.amount)
	if b.remaining() > 0 {
		a.open = append(a.open, b)
		slices.SortFunc(a.open, spendOrder)
	}
	return nil
}

// payDebt uses newly available credit before it can be spent again, whether credited or released from a freeze.
func (a *allocation) payDebt(b *batchState, available int64) {
	paid := min(a.unfunded, available)
	a.unfunded -= paid
	b.consumed += paid
	b.touched = b.touched || paid > 0
}

// writeOff takes an expiry from the batch it names.
func (a *allocation) writeOff(m movement) error {
	b, ok := a.batches[m.request]
	if !ok || m.debit <= 0 || m.debit > b.available() {
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
		if n == 0 {
			break
		}
		// A charged-back batch may hold a negative remainder. It stays in the balance, but it cannot fund a later
		// charge: giving it a negative "take" would spend another batch's credit to cancel the debt in this cache.
		available := b.available()
		if available <= 0 {
			continue
		}
		take := min(available, n)
		b.consumed += take
		b.touched = true
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

// loadAllocation reads the stored state of an account: where the replay stood, then the batches, then the place again,
// and returns errStale when it moved. The batches' replay state is written only by save, in the transaction
// that moves the place, and the place only moves forward; so a place that did not move across the batches' read means
// they are the ones stored with it. A replay that started from batches newer than its place would miss a batch another
// replay spent up in between, and fail on its credit, or on an expiry that no longer fits, before save's check of the
// place could drop it (#357). A caller that holds the account's balance row lock reads the latest state, and no other
// transaction can move it.
func loadAllocation(ctx context.Context, tx pg.Tx, account uuid.UUID) (*allocation, error) {
	if err := planLedgerPage(ctx, tx); err != nil {
		return nil, err
	}
	state, err := queries.AllocationState(ctx, tx, account)
	if err != nil {
		return nil, fmt.Errorf("billing: read the allocation of %s: %w", account, err)
	}
	loaded, err := queries.LiveBatches(ctx, tx, account)
	if err != nil {
		return nil, fmt.Errorf("billing: read the batches of %s: %w", account, err)
	}
	again, err := queries.AllocationState(ctx, tx, account)
	if err != nil {
		return nil, fmt.Errorf("billing: read the allocation of %s: %w", account, err)
	}
	if placeOf(again).compare(placeOf(state)) != 0 {
		return nil, errStale
	}
	return newAllocation(state, loaded), nil
}

// catchUpPage replays the next page of at most size transactions, no later than through, and reports whether
// the page was full, so that there may be more. The transactions from before the sequence come first; the page that
// ends them goes on into the numbered ones. A page is bounded by the index, not by the history.
func (a *allocation) catchUpPage(ctx context.Context, tx pg.Tx, account uuid.UUID, size int32, through int64) (full bool, err error) {
	// Read one row beyond the page so a page never ends on a reverse whose settle is the next row: the two must be
	// netted at the settle's place, and a page boundary between them would refund first and spend again, possibly
	// from a batch that expires at another time (docs/domains/billing.md「额度批次」).
	fetch := size + 1
	if a.at.seq == 0 {
		rows, err := queries.LegacyMovements(ctx, tx, LegacyMovementsParams{AccountID: account, AfterAt: a.at.at, AfterID: a.at.id, Max: fetch})
		if err != nil {
			return false, fmt.Errorf("billing: read the movements of %s: %w", account, err)
		}
		moves := legacyMovementsOf(rows)
		if len(rows) == int(fetch) {
			return true, a.applyPage(account, moves[:size], true)
		}
		if err := a.applyPage(account, moves, false); err != nil {
			return false, err
		}
	}
	rows, err := queries.Movements(ctx, tx, MovementsParams{AccountID: account, AfterSeq: a.at.seq, ThroughSeq: through, Max: fetch})
	if err != nil {
		return false, fmt.Errorf("billing: read the movements of %s: %w", account, err)
	}
	moves := movementsOf(rows)
	if len(rows) == int(fetch) {
		return true, a.applyPage(account, moves[:size], true)
	}
	return false, a.applyPage(account, moves, false)
}

// applyPage replays a page, holding back a trailing reverse when another row follows. The next page starts with it and
// its settle, so applyAll can net them.
func (a *allocation) applyPage(account uuid.UUID, moves []movement, more bool) error {
	if more && len(moves) > 1 {
		last := moves[len(moves)-1]
		if last.kind == "reverse" && last.revision != 0 {
			moves = moves[:len(moves)-1]
		}
	}
	return a.applyAll(account, moves)
}

func legacyMovementsOf(rows []LegacyMovementsRow) []movement {
	moves := make([]movement, len(rows))
	for i, r := range rows {
		moves[i] = movement{key: orderKey{seq: r.Seq, at: r.CreatedAt, id: r.ID}, kind: r.Kind, request: r.RequestID, debit: r.Debit, revision: r.Revision}
	}
	return moves
}

func movementsOf(rows []MovementsRow) []movement {
	moves := make([]movement, len(rows))
	for i, r := range rows {
		moves[i] = movement{key: orderKey{seq: r.Seq, at: r.CreatedAt, id: r.ID}, kind: r.Kind, request: r.RequestID, debit: r.Debit, revision: r.Revision}
	}
	return moves
}

// catchUp replays every transaction posted since the last one replayed.
func (a *allocation) catchUp(ctx context.Context, tx pg.Tx, account uuid.UUID, size int32) error {
	for {
		full, err := a.catchUpPage(ctx, tx, account, size, math.MaxInt64)
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
			batches.FreezeTotal = append(batches.FreezeTotal, b.freezeTotal)
			batches.UnfreezeTotal = append(batches.UnfreezeTotal, b.unfreezeTotal)
			batches.Refunded = append(batches.Refunded, b.refunded)
			batches.ChargedBack = append(batches.ChargedBack, b.chargedBack)
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

// replayAccount takes the money lock and checks both ledger-derived totals before a freeze or expiry can move money.
func replayAccount(ctx context.Context, tx pg.Tx, account uuid.UUID, size int32) (LockBalanceRow, *allocation, error) {
	balance, err := lockAccount(ctx, tx, account)
	if err != nil {
		return balance, nil, err
	}
	a, err := loadAllocation(ctx, tx, account)
	if err != nil {
		return balance, nil, err
	}
	if err := a.catchUp(ctx, tx, account, size); err != nil {
		return balance, nil, err
	}
	if a.funded() != balance.Balance || a.frozen() != balance.Frozen {
		return balance, nil, fmt.Errorf("billing: allocation of %s funds %d frozen %d, balance %d frozen %d", account,
			a.funded(), a.frozen(), balance.Balance, balance.Frozen)
	}
	return balance, a, nil
}
