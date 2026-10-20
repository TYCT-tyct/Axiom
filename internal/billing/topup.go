package billing

import (
	"context"
	"errors"
	"math/big"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

// topUpBufferPPM is the top-up buffer of a site that set none: 2%, about a card network's currency conversion spread,
// so the platform does not lose when the manual reference rate lags the market (docs/domains/billing.md「汇率」).
const topUpBufferPPM = 20_000

// ErrNoRate is a currency pair without a reference rate: payments does not offer the method.
var ErrNoRate = errors.New("billing: no exchange rate for the currency pair")

// Rate is the basis a top-up order locks: the reference version it read and the rate the user is charged at.
type Rate struct {
	Reference platform.ExchangeRate
	BufferPPM int64
	// Applied is how many units of to one unit of from costs: the reference raised by the buffer. An exact fraction,
	// as its numerator and denominator can exceed int64 (each reference term goes to 10¹⁸).
	Applied *big.Rat
}

// TopUpRate is the current reference rate from the site's credit currency to the paying currency, with the site's
// top-up buffer, for payments to lock in an order (internal/payments/README.md「billing 要提供的接口」). The rates are
// platform data, so tx is in the platform scope.
func TopUpRate(ctx context.Context, tx pg.Tx, site uuid.UUID, from, to platform.Currency) (Rate, error) {
	base, quote, err := ExchangePair(from.String(), to.String())
	if err != nil {
		return Rate{}, err
	}
	rows, err := queries.LatestExchangeRate(ctx, tx, LatestExchangeRateParams{Base: base, Quote: quote})
	if err != nil {
		return Rate{}, err
	}
	if len(rows) == 0 {
		return Rate{}, ErrNoRate
	}
	buffers, err := queries.TopUpBuffer(ctx, tx, site)
	if err != nil {
		return Rate{}, err
	}
	r := Rate{Reference: rows[0].Basis(), BufferPPM: topUpBufferPPM}
	if len(buffers) > 0 {
		r.BufferPPM = int64(buffers[0])
	}
	reference, _ := r.Reference.Between(from, to) // the pair was read for exactly these currencies
	r.Applied = reference.Mul(reference, big.NewRat(1_000_000+r.BufferPPM, 1_000_000))
	return r, nil
}
