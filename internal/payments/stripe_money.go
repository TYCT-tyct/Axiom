package payments

import (
	"errors"
	"math"
	"strings"

	"github.com/stripe/stripe-go/v85"

	"axiom/internal/platform"
)

func stripeAccount(c MerchantConfig) error {
	switch {
	case c.Client == nil || c.Currency == (platform.Currency{}):
		return errors.New("stripe: require client and currency")
	case c.Currency.MinorDigits() == 3:
		// Stripe takes three-decimal amounts only in whole tens (docs.stripe.com/currencies), which an exact total
		// in thousandths cannot always be.
		return errors.New("stripe: three-decimal currencies are not supported")
	default:
		return nil
	}
}

// stripeScale is how many of Stripe's amount units make one of ours. ISK and UGX are zero-decimal currencies that
// Stripe still writes with two decimals of 00 (docs.stripe.com/currencies#special-cases); every other currency the
// plugin accepts has the same minor unit at Stripe.
func stripeScale(c platform.Currency) int64 {
	switch c.String() {
	case "ISK", "UGX":
		return 100
	default:
		return 1
	}
}

// stripeAmount is m in Stripe's units for its currency.
func stripeAmount(m Money) (int64, bool) {
	scale := stripeScale(m.Currency)
	if m.Minor > math.MaxInt64/scale {
		return 0, false
	}
	return m.Minor * scale, true
}

// stripeMoney reads an amount Stripe reported; a currency other than the account's, or a fraction of our minor unit,
// is an invalid payment for review.
func stripeMoney(fallback platform.Currency, amount int64, currency stripe.Currency) Money {
	scale := stripeScale(fallback)
	if amount <= 0 || amount%scale != 0 {
		return Money{Currency: fallback, Minor: -1}
	}
	parsed, err := platform.ParseCurrency(strings.ToUpper(string(currency)))
	if err != nil || parsed != fallback {
		return Money{Currency: fallback, Minor: -1}
	}
	return Money{Currency: parsed, Minor: amount / scale}
}
