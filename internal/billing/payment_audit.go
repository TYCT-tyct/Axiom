package billing

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// PaymentCash is a merchant's net cash in a payment currency, in exact billionths (possibly wider than int64).
type PaymentCash struct{ Currency, Amount string }

// MerchantCash reads cash including refunds over a half-open UTC interval in the caller's consistent snapshot.
func MerchantCash(ctx context.Context, tx pg.Tx, merchant uuid.UUID, since, until time.Time) ([]PaymentCash, error) {
	rows, err := queries.MerchantCash(ctx, tx, MerchantCashParams{MerchantID: &merchant, Since: since, Until: until})
	if err != nil {
		return nil, err
	}
	out := make([]PaymentCash, len(rows))
	for i, r := range rows {
		out[i] = PaymentCash(r)
	}
	return out, nil
}

// MerchantBatches returns at most 100 paid batch IDs after the cursor, including batches with no matching order.
func MerchantBatches(ctx context.Context, tx pg.Tx, merchant, after uuid.UUID) ([]uuid.UUID, error) {
	return queries.MerchantBatches(ctx, tx, MerchantBatchesParams{MerchantID: &merchant, After: after})
}
