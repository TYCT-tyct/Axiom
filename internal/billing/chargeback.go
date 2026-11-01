package billing

import (
	"context"
	"errors"
	"fmt"
	"uuid"

	"axiom/internal/platform/pg"
)

// ErrRefundInFlight means a refund still holds credit of the batch, so a chargeback would take the same credit twice.
var ErrRefundInFlight = errors.New("billing: a refund is in flight")

// ErrChargebackCash means the cash the channel reports pulling back is not what the payment still owes after refunds
// and earlier chargebacks.
var ErrChargebackCash = errors.New("billing: the chargeback cash does not match the payment")

// Chargeback is a payment the channel pulled back. Cash is the net amount in the payment currency's minor units, as
// the channel reports it; the credit, fee and exchange parts are derived from the batch, never passed in.
type Chargeback struct {
	Batch uuid.UUID
	Cash  int64
}

// ChargebackAmounts is what a chargeback took: credits in billionths, cash and fee in payment minor units, and the
// revision that names it. A reversal reuses the same values and revision.
type ChargebackAmounts struct {
	Batch    uuid.UUID
	Credits  int64
	Revision int64
	RefundAmounts
}

// ChargeBack books a channel chargeback in tx, the payment's platform-scope transaction. It derives the returned
// credit from the batch and the cash and fee from the ledger, so a caller cannot take a different amount than the
// payment supports. A refund in flight returns ErrRefundInFlight; a cash amount that does not match returns
// ErrChargebackCash. Payments guards retries with its dispute row update in this same transaction; this function does
// not deduplicate calls.
//
//nolint:cyclop // validation, replay and posting are one money transaction
func ChargeBack(ctx context.Context, tx pg.Tx, c Chargeback) (ChargebackAmounts, error) {
	credit, a, b, err := c.load(ctx, tx)
	if err != nil {
		return ChargebackAmounts{}, err
	}
	if b.frozen() != 0 {
		return ChargebackAmounts{}, ErrRefundInFlight
	}
	history, err := queries.RefundHistory(ctx, tx, RefundHistoryParams{Batch: credit.ID, WithFee: true})
	if err != nil {
		return ChargebackAmounts{}, fmt.Errorf("billing: read returns of %s: %w", credit.ID, err)
	}
	returned, err := MinorUnits(credit.Payment.Currency, history.Returned)
	if err != nil {
		return ChargebackAmounts{}, err
	}
	fee, err := MinorUnits(credit.Payment.Currency, history.Fee)
	if err != nil {
		return ChargebackAmounts{}, err
	}
	amounts := ChargebackAmounts{
		Batch:         credit.ID,
		Credits:       credit.Amount - b.refunded - b.chargedBack,
		RefundAmounts: RefundAmounts{Returned: credit.Payment.Paid - returned, Fee: credit.Payment.Fee - fee}, //nolint:modernize // the embedded type is required in a keyed literal
	}
	if amounts.Credits < 0 || amounts.Returned < 0 || amounts.Fee < 0 || amounts.Fee > amounts.Returned {
		return ChargebackAmounts{}, fmt.Errorf("billing: payment of %s has %d credits, %d cash and %d fee left",
			credit.ID, amounts.Credits, amounts.Returned, amounts.Fee)
	}
	if c.Cash != amounts.Returned {
		return ChargebackAmounts{}, ErrChargebackCash
	}
	revision, err := queries.NextChargeBackRevision(ctx, tx, credit.ID)
	if err != nil {
		return ChargebackAmounts{}, fmt.Errorf("billing: read the next chargeback of %s: %w", credit.ID, err)
	}
	amounts.Revision = revision
	entries, err := returnEntries(credit, amounts.Credits, amounts.RefundAmounts)
	if err != nil {
		return ChargebackAmounts{}, err
	}
	if err := postChargeback(ctx, tx, a, credit, amounts, "chargeback", entries); err != nil {
		return ChargebackAmounts{}, err
	}
	return amounts, nil
}

// ReverseChargeBack gives back a chargeback's credit after the dispute is won. It posts the negation of the original
// entries with the original revision, so it can run once and cannot reverse a different dispute. The order and its
// state stay with payments; this only moves the money.
//
//nolint:cyclop // the same validation and replay as ChargeBack, in the other direction
func ReverseChargeBack(ctx context.Context, tx pg.Tx, amounts ChargebackAmounts) error {
	if amounts.Revision <= 0 || amounts.Credits < 0 || amounts.Returned < 0 || amounts.Fee < 0 || amounts.Fee > amounts.Returned {
		return fmt.Errorf("billing: reverse chargeback revision %d with %d credits", amounts.Revision, amounts.Credits)
	}
	credit, ok, err := ReadCredit(ctx, tx, amounts.Batch)
	if err != nil {
		return err
	}
	if !ok || credit.Source != "paid" || credit.Payment == nil {
		return fmt.Errorf("billing: batch %s is not paid credit", amounts.Batch)
	}
	_, a, err := replayAccount(ctx, tx, credit.Account, movementPage)
	if err != nil {
		return err
	}
	b, err := loadChargebackBatch(ctx, tx, a, credit.Account, amounts.Batch)
	if err != nil {
		return err
	}
	if b.chargedBack < amounts.Credits {
		return fmt.Errorf("billing: batch %s has %d charged back, not %d", amounts.Batch, b.chargedBack, amounts.Credits)
	}
	entries, err := returnEntries(credit, amounts.Credits, amounts.RefundAmounts)
	if err != nil {
		return err
	}
	return postChargeback(ctx, tx, a, credit, amounts, "chargeback_reverse", entries)
}

func (c Chargeback) load(ctx context.Context, tx pg.Tx) (CreditBatch, *allocation, *batchState, error) {
	if c.Cash <= 0 {
		return CreditBatch{}, nil, nil, fmt.Errorf("billing: chargeback cash must be positive: %d", c.Cash)
	}
	credit, ok, err := ReadCredit(ctx, tx, c.Batch)
	if err != nil {
		return CreditBatch{}, nil, nil, err
	}
	if !ok || credit.Source != "paid" || credit.Payment == nil {
		return CreditBatch{}, nil, nil, fmt.Errorf("billing: batch %s is not paid credit", c.Batch)
	}
	_, a, err := replayAccount(ctx, tx, credit.Account, movementPage)
	if err != nil {
		return CreditBatch{}, nil, nil, err
	}
	b, err := loadChargebackBatch(ctx, tx, a, credit.Account, c.Batch)
	if err != nil {
		return CreditBatch{}, nil, nil, err
	}
	if b.amount != credit.Amount || b.source != "paid" {
		return CreditBatch{}, nil, nil, fmt.Errorf("billing: batch %s is not open paid credit", c.Batch)
	}
	return credit, a, b, nil
}

// loadChargebackBatch returns the batch's replay state, loading a fully expired batch that LiveBatches omits. The
// balance is already locked by replayAccount; reading one row does not change the allocation place.
func loadChargebackBatch(ctx context.Context, tx pg.Tx, a *allocation, account, id uuid.UUID) (*batchState, error) {
	if b := a.batches[id]; b != nil {
		return b, nil
	}
	row, err := queries.ChargebackBatch(ctx, tx, ChargebackBatchParams{ID: id, AccountID: account})
	if err != nil {
		return nil, fmt.Errorf("billing: load chargeback batch %s: %w", id, err)
	}
	if row.Source != "paid" {
		return nil, fmt.Errorf("billing: batch %s is not paid credit", id)
	}
	b := &batchState{
		id: row.ID, site: row.SiteID, source: row.Source, expiresAt: row.ExpiresAt, amount: row.Amount,
		consumed: row.Consumed, expired: row.Expired, opened: orderKey{seq: row.OpenedSeq, at: row.OpenedAt, id: row.OpenedID},
		freezeTotal: row.FreezeTotal, unfreezeTotal: row.UnfreezeTotal, refunded: row.Refunded, chargedBack: row.ChargedBack,
	}
	a.restoreBatch(b)
	return b, nil
}

// postChargeback writes the event, replays it into the batch cache and saves the cache, all under the balance lock.
func postChargeback(ctx context.Context, tx pg.Tx, a *allocation, credit CreditBatch, amounts ChargebackAmounts,
	kind string, entries []line,
) error {
	if kind == "chargeback_reverse" {
		for i := range entries {
			entries[i].amount = -entries[i].amount
		}
	}
	e := event{site: credit.Site, account: credit.Account, kind: kind, request: credit.ID, revision: amounts.Revision}
	posted, err := post(ctx, tx, e, entries...)
	if err != nil {
		return err
	}
	if !posted {
		return fmt.Errorf("billing: %s of %s at %d already posted but not replayed", kind, credit.ID, e.revision)
	}
	if err := a.catchUp(ctx, tx, credit.Account, movementPage); err != nil {
		return err
	}
	return a.save(ctx, tx, credit.Account)
}
