package billing

import (
	"context"
	"fmt"
	"math"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// Freeze identifies paid credit to hold for a refund. Amount is in billionths of a credit; FreezeRefund may hold less.
type Freeze struct {
	Account, Batch uuid.UUID
	Amount         int64
}

// Refund settles frozen credit using the original payment and the first successful refund's fee policy.
type Refund struct {
	Freeze
	WithFee bool
}

// FreezeRefund holds up to f.Amount refundable credits in tx, the payment's platform-scope transaction. Payments
// guards retries with its order/refund row updates in this same transaction; these functions do not deduplicate calls.
// Reservations and existing freezes are protected, without using the balance floor's allowance for debt.
// Call PrepareRefund before opening tx or taking an order lock, so only the recent ledger tail needs this lock.
func FreezeRefund(ctx context.Context, tx pg.Tx, f Freeze) (int64, error) {
	balance, a, credit, err := f.load(ctx, tx)
	if err != nil {
		return 0, err
	}
	now, err := queries.Now(ctx, tx)
	if err != nil {
		return 0, fmt.Errorf("billing: read the refund clock: %w", err)
	}
	b := a.batches[f.Batch]
	if b == nil || credit.ExpiresAt != nil && !credit.ExpiresAt.After(now) {
		return 0, a.save(ctx, tx, f.Account)
	}
	balance.Floor = 0
	f.Amount = min(f.Amount, b.available(), room(balance, 0))
	if f.Amount == 0 {
		return 0, a.save(ctx, tx, f.Account)
	}
	if err := a.postRefund(ctx, tx, f, credit.Site, "freeze", nil); err != nil {
		return 0, err
	}
	return f.Amount, nil
}

// ReleaseRefund returns frozen credit to spending after a definitive failure. A batch past its expiry is released
// too: a subsequent expiry writes it off, after outstanding debt has been paid by the replay.
func ReleaseRefund(ctx context.Context, tx pg.Tx, f Freeze) error {
	_, a, credit, err := f.load(ctx, tx)
	if err != nil {
		return err
	}
	b := a.batches[f.Batch]
	if b == nil || f.Amount > b.frozen() {
		return fmt.Errorf("billing: release of %d exceeds the freeze of batch %s", f.Amount, f.Batch)
	}
	return a.postRefund(ctx, tx, f, credit.Site, "unfreeze", nil)
}

// SettleRefund converts frozen credit into a refund, even after expiry or spending down to the floor. Cash is booked
// in the original payment currency; exchange gain/loss balances each currency without using today's exchange rate.
// The caller must change its refund's state in tx so retries cannot settle a different freeze.
func SettleRefund(ctx context.Context, tx pg.Tx, r Refund) error {
	a, credit, amounts, err := r.quote(ctx, tx)
	if err != nil {
		return err
	}
	entries, err := r.entries(credit, amounts)
	if err != nil {
		return err
	}
	if err := queries.SetRefundPolicy(ctx, tx, SetRefundPolicyParams{Batch: r.Batch, WithFee: r.WithFee}); err != nil {
		return fmt.Errorf("billing: fix the refund policy of %s: %w", r.Batch, err)
	}
	return a.postRefund(ctx, tx, r.Freeze, credit.Site, "refund", entries)
}

// RefundRecordedAt reads the latest successful refund's ledger clock under the caller's batch serialization lock.
// Payments uses that exact timestamp as finished_at, so a transaction spanning UTC midnight reconciles on one day.
func RefundRecordedAt(ctx context.Context, tx pg.Tx, batch uuid.UUID) (time.Time, error) {
	return queries.RefundRecordedAt(ctx, tx, batch)
}

func (f Freeze) load(ctx context.Context, tx pg.Tx) (LockBalanceRow, *allocation, CreditBatch, error) {
	credit, err := f.credit(ctx, tx)
	if err != nil {
		return LockBalanceRow{}, nil, credit, err
	}
	balance, a, err := replayAccount(ctx, tx, f.Account, movementPage)
	return balance, a, credit, err
}

func (f Freeze) credit(ctx context.Context, tx pg.Tx) (CreditBatch, error) {
	if f.Amount <= 0 {
		return CreditBatch{}, fmt.Errorf("billing: refund credit must be positive: %d", f.Amount)
	}
	credit, ok, err := ReadCredit(ctx, tx, f.Batch)
	if err != nil {
		return credit, err
	}
	if !ok || credit.Account != f.Account || credit.Source != "paid" {
		return credit, fmt.Errorf("billing: batch %s is not paid credit of account %s", f.Batch, f.Account)
	}
	return credit, nil
}

// PrepareRefund advances an account in short transactions before payments opens its order/refund transaction.
// Scope must see the account; the caller supplies its deadline. Expiry and refunds share the same bounded replay.
func PrepareRefund(ctx context.Context, db *pg.DB, scope pg.Scope, f Freeze) error {
	if err := db.ReadTx(ctx, scope, func(tx pg.Tx) error {
		_, err := f.credit(ctx, tx)
		return err
	}); err != nil {
		return err
	}
	for more := true; more; {
		var err error
		if more, err = advanceAllocation(ctx, db, scope, f.Account, movementPage); err != nil {
			return fmt.Errorf("billing: prepare refund of %s: %w", f.Batch, err)
		}
	}
	return nil
}

// postRefund updates the synchronous admission counter and replays the event into the allocation cache atomically.
func (a *allocation) postRefund(ctx context.Context, tx pg.Tx, f Freeze, site uuid.UUID, kind string, entries []line) error {
	previous := *a.batches[f.Batch].refundTotal(kind)
	if f.Amount > math.MaxInt64-previous {
		return fmt.Errorf("billing: cumulative %s of batch %s exceeds the ledger", kind, f.Batch)
	}
	e := event{site: site, account: f.Account, kind: kind, request: f.Batch, revision: previous + f.Amount}
	posted, err := post(ctx, tx, e, entries...)
	if err != nil {
		return err
	}
	if !posted {
		return fmt.Errorf("billing: %s of batch %s at %d already posted but not replayed", kind, f.Batch, e.revision)
	}
	delta := -f.Amount
	if kind == "freeze" {
		delta = f.Amount
	}
	if err := queries.AddFrozen(ctx, tx, AddFrozenParams{AccountID: f.Account, Delta: delta}); err != nil {
		return fmt.Errorf("billing: move the frozen balance of %s: %w", f.Account, err)
	}
	if err := a.catchUp(ctx, tx, f.Account, movementPage); err != nil {
		return err
	}
	return a.save(ctx, tx, f.Account)
}

func (r Refund) entries(credit CreditBatch, amounts RefundAmounts) ([]line, error) {
	return returnEntries(credit, r.Amount, amounts)
}

// returnEntries books credit and the matching cash and fee as returned. Refunds and chargebacks share the shape; a
// reversal negates it.
func returnEntries(credit CreditBatch, credits int64, amounts RefundAmounts) ([]line, error) {
	// CreditPaid already checked that the whole payment fits the ledger, so both bounded parts fit too.
	scale, err := unitScale(credit.Payment.Currency)
	if err != nil {
		return nil, err
	}
	cash, fee := amounts.Returned*scale, amounts.Fee*scale
	if credit.RateID == (uuid.UUID{}) {
		return []line{
			{bookUserBalance, credits, siteCredits},
			{bookFeeIncome, fee, siteCredits},
			{bookCash, -cash, siteCredits},
			{bookExchange, cash - fee - credits, siteCredits},
		}, nil
	}
	in := credit.Payment.Currency.String()
	return []line{
		{bookUserBalance, credits, siteCredits},
		{bookExchange, -credits, siteCredits},
		{bookFeeIncome, fee, in},
		{bookCash, -cash, in},
		{bookExchange, cash - fee, in},
	}, nil
}
