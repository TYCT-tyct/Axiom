package billing

import (
	"context"
	"fmt"
	"time"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// CreditBatch is a credit as the admin shows a grant: when it was made, how much, when what is left of it ends, and
// for paid credit what was paid, where, and the exchange rate version it was converted at (zero in one currency).
type CreditBatch struct {
	ID, Site, Account uuid.UUID
	Source            string // gift or paid
	Amount            int64  // billionths of a credit
	ExpiresAt         *time.Time
	CreatedAt         time.Time
	Payment           *Payment // nil for a gift
	Channel           Channel
	RateID            uuid.UUID
}

// ReadCredit is the batch AddCredit or CreditPaid made with id, read in tx, which must hold the platform scope (or the
// account's); ok is false when there is none.
func ReadCredit(ctx context.Context, tx pg.Tx, id uuid.UUID) (batch CreditBatch, ok bool, err error) {
	rows, err := queries.ReadCreditBatch(ctx, tx, id)
	if err != nil {
		return CreditBatch{}, false, fmt.Errorf("billing: read the credit batch: %w", err)
	}
	if len(rows) == 0 {
		return CreditBatch{}, false, nil
	}
	r := rows[0]
	batch = CreditBatch{
		ID: r.ID, Site: r.SiteID, Account: r.AccountID, Source: r.Source, Amount: r.Amount, ExpiresAt: r.ExpiresAt,
		CreatedAt: r.CreatedAt, Channel: Channel{Offline: r.OfflineChannel},
	}
	if r.Source == "paid" { // credit_batches_source_check: a paid batch has its currency, paid and fee
		currency, err := platform.ParseCurrency(r.Currency)
		if err != nil {
			return CreditBatch{}, false, fmt.Errorf("billing: read the credit batch %s: %w", id, err)
		}
		batch.Payment = &Payment{Currency: currency, Paid: *r.Paid, Fee: *r.Fee}
	}
	if r.MerchantID != nil {
		batch.Channel.Merchant = *r.MerchantID
	}
	if r.RateID != nil {
		batch.RateID = *r.RateID
	}
	return batch, true, nil
}
