package payments

import (
	"errors"
	"math/big"

	"axiom/internal/platform"
)

const (
	creditScale = 1_000_000_000 // credits are billionths of the site's currency (docs/domains/billing.md「钱的表示」)
	ppmScale    = 1_000_000
	maxFeePPM   = 500_000 // 50%: no real channel charges more, and the worked-back total would multiply
)

// ErrOutOfRange refuses a top-up whose amounts do not fit: credits × rate or the total beyond int64 minor units.
var ErrOutOfRange = errors.New("payments: the amount is out of range")

// Fee is a merchant method's fee: a proportion of the total the user pays, in millionths, plus a fixed amount in
// minor units of the merchant's currency.
type Fee struct {
	PPM   int32
	Fixed int64
}

// Quote is what the user pays for some credits through one merchant method.
type Quote struct {
	Total Money
	Fee   int64 // Total minus the top-up part ⌈credits × applied rate⌉, for showing and splitting the entries
}

// quote works the total back from the net amount, since the channel takes its fee from the total:
//
//	total = ⌈(credits × applied × 10^digits ÷ 10⁹ + fixed) ÷ (1 − ppm ÷ 10⁶)⌉
//
// exactly, rounded up to the minor unit once (docs/domains/payments.md「渠道手续费」). sub2api adds the fee to the
// amount instead (backend/internal/payment/fee.go:12-22), which leaves less than the amount after the channel's cut.
// applied is how many units of the merchant's currency one unit of the site's buys; nil is the same currency.
func quote(credits int64, applied *big.Rat, currency platform.Currency, fee Fee) (Quote, error) {
	if credits <= 0 || fee.PPM < 0 || fee.PPM > maxFeePPM || fee.Fixed < 0 {
		return Quote{}, ErrOutOfRange
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits(currency))), nil)
	principal := new(big.Rat).SetFrac(new(big.Int).Mul(big.NewInt(credits), scale), big.NewInt(creditScale))
	if applied != nil {
		principal.Mul(principal, applied)
	}
	part := ceil(principal)
	if !part.IsInt64() {
		return Quote{}, ErrOutOfRange
	}
	gross := principal.Add(principal, new(big.Rat).SetInt64(fee.Fixed))
	gross.Mul(gross, big.NewRat(ppmScale, ppmScale-int64(fee.PPM)))
	total := ceil(gross)
	if !total.IsInt64() {
		return Quote{}, ErrOutOfRange
	}
	return Quote{Total: Money{currency, total.Int64()}, Fee: total.Int64() - part.Int64()}, nil
}

// ceil rounds a non-negative fraction up to an integer.
func ceil(r *big.Rat) *big.Int {
	q, m := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	if m.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}
