package billing

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
)

// topUpBufferPPM is the top-up buffer of a site that set none: 2%, about a card network's currency conversion spread,
// so the platform does not lose when the manual reference rate lags the market (docs/domains/billing.md「汇率」).
const topUpBufferPPM = 20_000

// TopUpBufferPPM is the buffer a site charges on a top-up: its own value, or the default when it set none.
func TopUpBufferPPM(ctx context.Context, tx pg.Tx, site uuid.UUID) (int32, error) {
	buffers, err := queries.TopUpBuffer(ctx, tx, site)
	if err != nil {
		return 0, err
	}
	if len(buffers) == 0 {
		return topUpBufferPPM, nil
	}
	return buffers[0], nil
}

// SetTopUpBuffer stores a site's buffer and its audit entry together. Zero is a deliberate choice (no buffer), not a
// request to inherit the default; the table has no row only until the first save.
func SetTopUpBuffer(ctx context.Context, tx pg.Tx, actor audit.Actor, site uuid.UUID, ppm int32, reason string) error {
	if ppm < 0 || ppm > 100_000 {
		return fmt.Errorf("billing: top-up buffer %d is outside 0 to 100000", ppm)
	}
	// Serialize the first insert and every later change so the audit entry's before value is the one it replaced.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "billing.top_up_buffer:"+site.String()); err != nil {
		return err
	}
	before, err := TopUpBufferPPM(ctx, tx, site)
	if err != nil {
		return err
	}
	if err := queries.SetTopUpBuffer(ctx, tx, SetTopUpBufferParams{SiteID: site, Ppm: ppm}); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Event{
		Actor: actor, Action: "billing.top_up_buffer.update", Reason: reason,
		Target: audit.Target{Module: "billing", Kind: "site", ID: site.String(), Site: site},
		Detail: map[string]any{"before": before, "after": ppm},
	})
}

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
