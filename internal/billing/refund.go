package billing

import (
	"context"
	"fmt"
	"math"
	"uuid"

	"axiom/internal/platform/pg"
)

// Freeze identifies paid credit to hold for a refund. Amount is in billionths of a credit; FreezeRefund may hold less.
type Freeze struct {
	Account, Batch uuid.UUID
	Amount         int64
}

// Refund settles Amount frozen credits for Returned minor units of the batch's original payment currency.
// Payments calculates Returned from the order's paid amount, fee policy and previous refunds, including rounding.
type Refund struct {
	Freeze
	Returned int64
}

// FreezeRefund holds up to f.Amount refundable credits in tx, the payment's platform-scope transaction. Payments
// guards retries with its order/refund row updates in this same transaction; these functions do not deduplicate calls.
// Reservations and existing freezes are protected, without using the balance floor's allowance for debt.
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
	if err := a.postRefund(ctx, tx, f, credit.Site, "freeze", b.freezeTotal, nil); err != nil {
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
	return a.postRefund(ctx, tx, f, credit.Site, "unfreeze", b.unfreezeTotal, nil)
}

// SettleRefund converts frozen credit into a refund, even after expiry or spending down to the floor. Cash is booked
// in the original payment currency; exchange gain/loss balances each currency without using today's exchange rate.
// The caller must change its refund's state in tx so retries cannot settle a different freeze.
func SettleRefund(ctx context.Context, tx pg.Tx, r Refund) error {
	_, a, credit, err := r.Freeze.load(ctx, tx)
	if err != nil {
		return err
	}
	b := a.batches[r.Batch]
	if b == nil || r.Amount > b.frozen() {
		return fmt.Errorf("billing: refund of %d exceeds the freeze of batch %s", r.Amount, r.Batch)
	}
	entries, err := r.entries(credit)
	if err != nil {
		return err
	}
	return a.postRefund(ctx, tx, r.Freeze, credit.Site, "refund", b.refunded, entries)
}

func (f Freeze) load(ctx context.Context, tx pg.Tx) (LockBalanceRow, *allocation, CreditBatch, error) {
	if f.Amount <= 0 {
		return LockBalanceRow{}, nil, CreditBatch{}, fmt.Errorf("billing: refund credit must be positive: %d", f.Amount)
	}
	balance, a, err := replayAccount(ctx, tx, f.Account, movementPage)
	if err != nil {
		return balance, nil, CreditBatch{}, err
	}
	credit, ok, err := ReadCredit(ctx, tx, f.Batch)
	if err != nil {
		return balance, nil, credit, err
	}
	if !ok || credit.Account != f.Account || credit.Source != "paid" {
		return balance, nil, credit, fmt.Errorf("billing: batch %s is not paid credit of account %s", f.Batch, f.Account)
	}
	return balance, a, credit, nil
}

// postRefund updates the synchronous admission counter and replays the event into the allocation cache atomically.
func (a *allocation) postRefund(ctx context.Context, tx pg.Tx, f Freeze, site uuid.UUID, kind string, previous int64, entries []line) error {
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

func (r Refund) entries(credit CreditBatch) ([]line, error) {
	scale, err := unitScale(credit.Payment.Currency)
	if err != nil {
		return nil, err
	}
	if r.Returned < 0 || r.Returned > credit.Payment.Paid || r.Returned > math.MaxInt64/scale {
		return nil, fmt.Errorf("billing: refund cash of %d does not fit payment of batch %s", r.Returned, r.Batch)
	}
	cash := r.Returned * scale
	if credit.RateID == (uuid.UUID{}) {
		return []line{
			{bookUserBalance, r.Amount, siteCredits}, {bookCash, -cash, siteCredits},
			{bookExchange, cash - r.Amount, siteCredits},
		}, nil
	}
	in := credit.Payment.Currency.String()
	return []line{
		{bookUserBalance, r.Amount, siteCredits}, {bookExchange, -r.Amount, siteCredits},
		{bookCash, -cash, in}, {bookExchange, cash, in},
	}, nil
}
