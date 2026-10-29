package platform

import (
	"math/big"
	"testing"
)

func TestParseExact(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"0", "-0", "12345678901234567890.123456789012345", "-12.000000000000001",
		"170141183460469231731687.303715884105727", "-170141183460469231731687.303715884105728",
	} {
		t.Run(text, func(t *testing.T) {
			got, err := ParseExact(text)
			want, _ := new(big.Rat).SetString(text)
			if err != nil || got.Rat().Cmp(want) != 0 {
				t.Fatalf("ParseExact(%q) = %v, %v; want %v", text, got.Rat(), err, want)
			}
		})
	}
	for _, text := range []string{
		"", "+1", "1e2", "1/2", ".5", "1.", "NaN", "0.0000000000000001",
		"170141183460469231731687.303715884105728", "-170141183460469231731687.303715884105729",
	} {
		if _, err := ParseExact(text); err == nil {
			t.Errorf("ParseExact(%q) accepted", text)
		}
	}
}
