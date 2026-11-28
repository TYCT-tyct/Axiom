package platform

import (
	"math/big"
	"strings"
	"testing"
)

func TestCeilExact(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want string }{
		{"0", "0"},
		{"0.95", "0.95"},
		{"1.2e-5", "0.000012"},
		{"1/3", "0.333333333333334"},
		{"0.0000000000000001", "0.000000000000001"},
		{"-1/3", "-0.333333333333333"},
	} {
		r, _ := new(big.Rat).SetString(c.in)
		got, err := CeilExact(r)
		want, _ := new(big.Rat).SetString(c.want)
		if err != nil || got.Rat().Cmp(want) != 0 {
			t.Errorf("CeilExact(%s) = %v, %v; want %s", c.in, got.Rat().FloatString(15), err, c.want)
		}
	}
	if _, err := CeilExact(new(big.Rat).SetInt(new(big.Int).Lsh(big.NewInt(1), 120))); err == nil {
		t.Error("CeilExact accepted a value past 128 bits")
	}
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"0.0000000000000001", "0.000000000000001", true},
		{"1.5E-5", "0.000015", true},
		{"0.95", "0.95", true},
		{"1e31", "", false},
		{"1e999999999", "", false},
		{"1/3", "", false},
		{"x", "", false},
		{"0." + strings.Repeat("1", 64), "", false},
	} {
		got, err := CeilDecimal(c.in)
		if want, _ := new(big.Rat).SetString(c.want); (err == nil) != c.ok || c.ok && got.Rat().Cmp(want) != 0 {
			t.Errorf("CeilDecimal(%q) = %v, %v; want %q", c.in, got.Rat(), err, c.want)
		}
	}
}

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
