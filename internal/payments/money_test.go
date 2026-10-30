package payments

import (
	"testing"

	"axiom/internal/platform"
)

func TestMerchantAmountsRoundTripMinorUnits(t *testing.T) {
	cases := []struct {
		currency string
		minor    int64
		amount   string
	}{
		{"USD", 310, "3100000000"},
		{"CNY", 1, "10000000"},
		{"JPY", 1, "1000000000"},
		{"KWD", 1234, "1234000000"},
		{"USD", 0, "0"},
	}
	for _, c := range cases {
		currency, err := platform.ParseCurrency(c.currency)
		if err != nil {
			t.Fatal(err)
		}
		amount := AmountFromMinor(currency, c.minor)
		if amount != c.amount {
			t.Errorf("%s %d = %s, want %s", c.currency, c.minor, amount, c.amount)
		}
		back, ok := MinorFromAmount(currency, amount)
		if !ok || back != c.minor {
			t.Errorf("%s %s = %d, %v; want %d", c.currency, amount, back, ok, c.minor)
		}
	}
	usd, err := platform.ParseCurrency("USD")
	if err != nil {
		t.Fatal(err)
	}
	for _, amount := range []string{"1", "-10000000", "9223372036854775807000000000", "x"} {
		if _, ok := MinorFromAmount(usd, amount); ok {
			t.Errorf("%s was accepted as whole minor units", amount)
		}
	}
}
