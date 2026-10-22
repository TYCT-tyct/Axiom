package billing

import (
	"context"
	"fmt"
	"math/big"

	"axiom/internal/platform/pg"
)

// RefundAmounts is the cash to return and its fee-income reversal, in original-payment minor units.
type RefundAmounts struct{ Returned, Fee int64 }

// QuoteRefund computes the channel amount inside the freeze transaction. It neither books money nor fixes policy.
// Payments persists this quote and permits only one refund in flight per batch, so settlement computes the same amount.
func QuoteRefund(ctx context.Context, tx pg.Tx, r Refund) (RefundAmounts, error) {
	_, _, amounts, err := r.quote(ctx, tx)
	return amounts, err
}

func (r Refund) quote(ctx context.Context, tx pg.Tx) (*allocation, CreditBatch, RefundAmounts, error) {
	_, a, credit, err := r.load(ctx, tx)
	if err != nil {
		return nil, credit, RefundAmounts{}, err
	}
	b := a.batches[r.Batch]
	if b == nil || r.Amount > b.frozen() {
		return nil, credit, RefundAmounts{}, fmt.Errorf("billing: refund of %d exceeds the freeze of batch %s", r.Amount, r.Batch)
	}
	history, err := queries.RefundHistory(ctx, tx, RefundHistoryParams{Batch: r.Batch, WithFee: r.WithFee})
	if err != nil {
		return nil, credit, RefundAmounts{}, fmt.Errorf("billing: read refunds of %s: %w", r.Batch, err)
	}
	amounts, err := r.amounts(credit, b.refunded, history)
	return a, credit, amounts, err
}

func (r Refund) amounts(credit CreditBatch, refunded int64, history RefundHistoryRow) (RefundAmounts, error) {
	net := credit.Payment.Paid - credit.Payment.Fee
	if r.WithFee {
		net = credit.Payment.Paid
	}
	prior, err := r.history(credit, net, history)
	if err != nil {
		return RefundAmounts{}, err
	}
	returned, fee := prior.Returned, prior.Fee
	amount := refundShare(r.Amount, net, credit.Amount)
	if r.Amount == credit.Amount-refunded {
		amount = net - returned
	}
	if amount > net-returned {
		return RefundAmounts{}, fmt.Errorf("billing: cumulative refund exceeds payment of %s", r.Batch)
	}
	part := int64(0)
	if r.WithFee {
		// Allocate fees by cumulative cash, so each part is <= its cash even with many one-minor-unit refunds.
		part = refundShare(returned+amount, credit.Payment.Fee, credit.Payment.Paid) - fee
	}
	if part < 0 || part > amount || !r.WithFee && fee != 0 {
		return RefundAmounts{}, fmt.Errorf("billing: refunded fees do not fit payment of %s", r.Batch)
	}
	return RefundAmounts{Returned: amount, Fee: part}, nil
}

func (r Refund) history(credit CreditBatch, net int64, history RefundHistoryRow) (RefundAmounts, error) {
	returned, err := MinorUnits(credit.Payment.Currency, history.Returned)
	if err != nil {
		return RefundAmounts{}, err
	}
	fee, err := MinorUnits(credit.Payment.Currency, history.Fee)
	if err != nil {
		return RefundAmounts{}, err
	}
	if history.WithFee != r.WithFee {
		return RefundAmounts{}, fmt.Errorf("billing: refund fee policy of %s was fixed by its first successful refund", r.Batch)
	}
	if returned < 0 || returned > net || fee < 0 || fee > returned {
		return RefundAmounts{}, fmt.Errorf("billing: refund history exceeds payment of %s", r.Batch)
	}
	return RefundAmounts{Returned: returned, Fee: fee}, nil
}

// refundShare floors exactly once; the product can exceed int64 even though its quotient fits.
func refundShare(amount, paid, credited int64) int64 {
	var n big.Int
	n.Mul(big.NewInt(amount), big.NewInt(paid))
	n.Quo(&n, big.NewInt(credited))
	return n.Int64()
}
