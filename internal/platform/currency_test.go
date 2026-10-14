package platform

import "testing"

func TestParseCurrency(t *testing.T) {
	for _, in := range []string{"USD", "CNY"} {
		if c, err := ParseCurrency(in); err != nil || c.String() != in {
			t.Errorf("ParseCurrency(%q) = %v, %v", in, c, err)
		}
	}
	for _, in := range []string{"", "usd", "US", "USDD", "U1D", "ÜSD"} {
		if c, err := ParseCurrency(in); err == nil {
			t.Errorf("ParseCurrency(%q) = %v, want an error", in, c)
		}
	}
}
