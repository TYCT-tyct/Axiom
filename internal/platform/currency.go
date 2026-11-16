package platform

import (
	"errors"
	"strings"
)

// Currency is a three-letter currency code in upper case, such as USD or CNY.
type Currency struct{ code string }

// CurrencyNone is ISO 4217's XXX, "no currency": what a supplier's cost plan is in, its amounts being the supplier's own
// unit (docs/domains/billing.md「供应商成本」).
var CurrencyNone = Currency{code: "XXX"}

// ParseCurrency accepts exactly three ASCII upper-case letters.
func ParseCurrency(s string) (Currency, error) {
	if len(s) != 3 || strings.Trim(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return Currency{}, errors.New("currency: want three upper-case letters such as USD")
	}
	return Currency{s}, nil
}

func (c Currency) String() string { return c.code }

// MinorDigits is the ISO 4217 minor-unit precision, without payment-provider exceptions for ISK and UGX.
// Other currency codes keep the payment protocol's two-digit default.
func (c Currency) MinorDigits() int {
	switch c.code {
	case "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "MGA", "PYG", "RWF", "UGX", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	default:
		return 2
	}
}
