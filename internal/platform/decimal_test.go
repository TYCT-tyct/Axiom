package platform

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// TestExactAppendDecimal pins the canonical text of an amount against the fraction it stands for: the shortest plain
// decimal that holds it, with no rounding, for both ends of the 128-bit range and for the paths that carry a sign and
// a fraction the 64-bit division cannot hold.
func TestExactAppendDecimal(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		e    Exact
		text string
	}{
		{Exact{}, "0"},
		{Exact{0, 1}, "0.000000000000001"},
		{AmountExact(500_000_000), "0.5"},
		{Exact{-1, 1<<64 - 1}, "-0.000000000000001"},
		{AmountExact(1), "0.000000001"},
		{AmountExact(-1_234_567_890_123), "-1234.567890123"},
		{Exact{0, priceScale}, "1"},
		{maxExact, "170141183460469231731687.303715884105727"},
		{minExact, "-170141183460469231731687.303715884105728"},
	} {
		if got := string(c.e.AppendDecimal(nil)); got != c.text {
			t.Errorf("%v.AppendDecimal() = %q, want %q", c.e, got, c.text)
		}
	}
}

// TestExactAppendDecimalMatchesTheFraction: the text is the one the exact fraction prints, so a call that reports a
// cost reports the amount that was charged. Random amounts cover both words and the sign, and appending after a
// prefix keeps the prefix.
func TestExactAppendDecimalMatchesTheFraction(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 2))                                            //nolint:gosec // G404: amounts for a formatting test, not a secret
	amounts := []Exact{{}, {0, 1}, {1, 0}, {-1, 1<<64 - 1}, maxExact, minExact} //nolint:exhaustruct // an Exact is its two words
	for range 500 {
		amounts = append(amounts, Exact{r.Int64(), r.Uint64()})
	}
	for _, e := range amounts {
		want := strings.TrimRight(strings.TrimRight(e.Rat().FloatString(15), "0"), ".")
		if got := string(e.AppendDecimal(nil)); got != want {
			t.Fatalf("%v.AppendDecimal() = %q, want %q", e, got, want)
		}
		if got := string(e.AppendDecimal([]byte("cost:"))); got != "cost:"+want {
			t.Fatalf("%v appended after a prefix is %q, want %q", e, got, "cost:"+want)
		}
	}
}
