package billing

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// RedeemCredit is credit a redeem code bought (docs/domains/payments.md「兑换码」): the site issued the code outside the
// payment channels, so the credit is booked like a gift, as its own source so the batch and the audit entry say where it
// came from. ID is the redemption's, which makes it idempotent: the same ID again with the same amounts writes nothing,
// with others it is ErrCreditConflict. ExpiresAt, when set, is when what is left of the batch lapses.
type RedeemCredit struct {
	ID, Site, Account uuid.UUID
	Amount            int64 // billionths of a credit, positive
	ExpiresAt         *time.Time
	Actor             audit.Actor
	Reason            string
}

// CreditRedeem records c in tx, which must be in the platform scope, as the audit log requires: the batch, the ledger
// transaction that credits the balance, and the audit entry, all or none.
func CreditRedeem(ctx context.Context, tx pg.Tx, c RedeemCredit) error {
	if c.Amount <= 0 {
		return fmt.Errorf("billing: a credit of %d", c.Amount)
	}
	posted, err := addBatch(ctx, tx, InsertCreditBatchParams{
		ID: c.ID, SiteID: c.Site, AccountID: c.Account, Source: "redeem", Amount: c.Amount, ExpiresAt: c.ExpiresAt,
	}, []line{{bookGiftExpense, c.Amount, siteCredits}, {bookUserBalance, -c.Amount, siteCredits}}, c.Actor, c.Reason, map[string]any{})
	if err != nil || posted {
		return err
	}
	return c.same(ctx, tx)
}

// same answers c's ID posted before: nothing to do if its batch is c's, ErrCreditConflict if it is anything else.
func (c RedeemCredit) same(ctx context.Context, tx pg.Tx) error {
	b, ok, err := ReadCredit(ctx, tx, c.ID)
	if err != nil {
		return err
	}
	sameExpiry := b.ExpiresAt == nil && c.ExpiresAt == nil ||
		b.ExpiresAt != nil && c.ExpiresAt != nil && b.ExpiresAt.Equal(*c.ExpiresAt)
	if ok && b.Account == c.Account && b.Site == c.Site && b.Source == "redeem" && b.Amount == c.Amount && sameExpiry {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrCreditConflict, c.ID)
}
