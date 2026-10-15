package platform

import (
	"math"
	"math/big"
	"regexp"
	"testing"
)

func TestParseUnitPrice(t *testing.T) {
	for in, want := range map[string]string{
		"0":                    "0",
		"0.000":                "0",
		"0.000003":             "0.000003",
		"00012.50":             "12.5",
		"0.000000000000001":    "0.000000000000001",
		"1000":                 "1000",
		"1000.000000000000000": "1000",
	} {
		p, err := ParseUnitPrice(in)
		if err != nil || p.String() != want {
			t.Errorf("ParseUnitPrice(%q) = %v, %v; want %s", in, p, err, want)
		}
	}
	for _, in := range []string{
		"", ".5", "5.", "-1", "-0", "+1", "0.+5", "1e3", "1e-6", "1E3", "0x10", "Inf", "NaN", " 1", "1 ", "1_000", "1,5", "1.2.3", "١",
		"0.0000000000000001", "0.0000000000000000", // 16 decimals, even when they are zeros
		"1000.000000000000001", "1001", "99999999999999999999999", // above the bound
	} {
		if p, err := ParseUnitPrice(in); err == nil {
			t.Errorf("ParseUnitPrice(%q) = %v, want an error", in, p)
		}
	}
}

var (
	priceText = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,15})?$`)
	canonical = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]*[1-9])?$`)
	unit      = new(big.Rat).SetFrac64(1, 1e15)
)

// FuzzParseUnitPrice checks the parser against math/big: text of the accepted shape whose value is at most 1000 parses
// to that exact value and formats back to the same price; everything else is rejected.
func FuzzParseUnitPrice(f *testing.F) {
	for _, s := range []string{"0", "0.000003", "1000", "1000.000000000000001", "0.0000000000000001", "-1", "1e-6", "3/4"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := ParseUnitPrice(s)
		want, ok := new(big.Rat), priceText.MatchString(s)
		if ok {
			want.SetString(s)
			ok = want.Cmp(big.NewRat(1000, 1)) <= 0
		}
		if !ok {
			if err == nil {
				t.Fatalf("ParseUnitPrice(%q) = %v, want an error", s, p)
			}
			return
		}
		if err != nil || p.Mul(1).Rat().Cmp(want) != 0 {
			t.Fatalf("ParseUnitPrice(%q) = %v, %v; want %s", s, p, err, want.FloatString(15))
		}
		if back, err := ParseUnitPrice(p.String()); err != nil || back != p || !canonical.MatchString(p.String()) {
			t.Fatalf("%q formats as %q, which parses to %v, %v", s, p.String(), back, err)
		}
	})
}

// FuzzUnitPriceString covers the whole domain, not only the text a fuzzer happens to write: every price formats as
// canonical text that parses back to it.
func FuzzUnitPriceString(f *testing.F) {
	f.Add(uint64(0))
	f.Add(uint64(maxUnitPrice))
	f.Add(uint64(3_000_000_000))
	f.Fuzz(func(t *testing.T, n uint64) {
		p := UnitPrice{int64(n % (maxUnitPrice + 1))} //nolint:gosec // below maxUnitPrice + 1
		s := p.String()
		want := new(big.Rat).Mul(new(big.Rat).SetInt64(p.n), unit)
		got, ok := new(big.Rat).SetString(s)
		if !canonical.MatchString(s) || !ok || got.Cmp(want) != 0 {
			t.Fatalf("%d units format as %q", p.n, s)
		}
		if back, err := ParseUnitPrice(s); err != nil || back != p {
			t.Fatalf("%q parses to %v, %v; want %v", s, back, err, p)
		}
	})
}

func FuzzMul(f *testing.F) {
	// The count is the fuzzed value shifted right by one, so these seeds are counts 1 and math.MaxInt64.
	f.Add(uint64(1), uint64(2))
	f.Add(uint64(maxUnitPrice), uint64(math.MaxUint64))
	f.Fuzz(func(t *testing.T, price, count uint64) {
		p, q := UnitPrice{int64(price % (maxUnitPrice + 1))}, int64(count>>1) //nolint:gosec // both fit in int64
		want := new(big.Rat).SetFrac(new(big.Int).Mul(big.NewInt(p.n), big.NewInt(q)), big.NewInt(1e15))
		if got := p.Mul(q).Rat(); got.Cmp(want) != 0 {
			t.Fatalf("%d × %d = %s, want %s", p.n, q, got, want)
		}
	})
}

func TestMulRejectsNegativeCount(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Mul(-1) did not panic")
		}
	}()
	UnitPrice{1}.Mul(-1)
}

var (
	maxExact = Exact{math.MaxInt64, math.MaxUint64}
	minExact = Exact{math.MinInt64, 0}
)

// FuzzAdd checks 128-bit addition against math/big over the whole signed range: the sum is exact, and it is an error
// exactly when math/big says the sum leaves the range.
func FuzzAdd(f *testing.F) {
	for _, pair := range [][2]Exact{
		{maxExact, {0, 1}},          // overflows
		{maxExact, {}},              // the largest value itself
		{minExact, {-1, 1<<64 - 1}}, // minus one: overflows
		{maxExact, minExact},        // -1
		{{0, 1<<64 - 1}, {0, 1}},    // carries into the high word
		{{-1, 0}, {0, 1<<64 - 1}},
	} {
		f.Add(pair[0].hi, pair[0].lo, pair[1].hi, pair[1].lo)
	}
	lowest := new(big.Rat).SetFrac(new(big.Int).Lsh(big.NewInt(-1), 127), big.NewInt(1e15))
	highest := new(big.Rat).Sub(new(big.Rat).Neg(lowest), unit)
	f.Fuzz(func(t *testing.T, ahi int64, alo uint64, bhi int64, blo uint64) {
		a, b := Exact{ahi, alo}, Exact{bhi, blo}
		want := new(big.Rat).Add(a.Rat(), b.Rat())
		got, err := a.Add(b)
		if want.Cmp(lowest) < 0 || want.Cmp(highest) > 0 {
			if err == nil {
				t.Fatalf("%v + %v = %v, want an overflow", a, b, got)
			}
			return
		}
		if err != nil || got.Rat().Cmp(want) != 0 {
			t.Fatalf("%v + %v = %v, %v; want %s", a, b, got, err, want)
		}
	})
}

// FuzzCmp checks the comparison against math/big over the whole signed range, both words' edges seeded.
func FuzzCmp(f *testing.F) {
	for _, pair := range [][2]Exact{{maxExact, minExact}, {{0, 1 << 63}, {0, 1}}, {{-1, 1<<64 - 1}, {}}, {{1, 0}, {0, 1<<64 - 1}}} {
		f.Add(pair[0].hi, pair[0].lo, pair[1].hi, pair[1].lo)
	}
	f.Fuzz(func(t *testing.T, ahi int64, alo uint64, bhi int64, blo uint64) {
		a, b := Exact{ahi, alo}, Exact{bhi, blo}
		if got, want := a.Cmp(b), a.Rat().Cmp(b.Rat()); got != want {
			t.Fatalf("%v.Cmp(%v) = %d, want %d", a, b, got, want)
		}
	})
}

// FuzzAmount checks both conversions against math/big over the whole range: an amount converts exactly, and an exact
// value rounds up to the next billionth, or panics when that is beyond int64.
func FuzzAmount(f *testing.F) {
	for _, pair := range [][2]int64{{math.MinInt64, 0}, {math.MaxInt64, -1}, {-1, 1}, {0, 0}} {
		f.Add(pair[0], pair[1], uint64(1))
	}
	f.Fuzz(func(t *testing.T, n, hi int64, lo uint64) {
		billionth := big.NewRat(1, 1e9)
		if got, want := AmountExact(n).Rat(), new(big.Rat).Mul(big.NewRat(n, 1), billionth); got.Cmp(want) != 0 {
			t.Fatalf("AmountExact(%d) = %s, want %s", n, got, want)
		}
		e := Exact{hi, lo}
		q := new(big.Rat).Quo(e.Rat(), billionth)
		ceil := new(big.Int).Neg(new(big.Int).Div(new(big.Int).Neg(q.Num()), q.Denom())) // Div floors
		got, panicked := func() (got int64, panicked bool) {
			defer func() { panicked = recover() != nil }()
			return e.CeilAmount(), false
		}()
		if panicked != !ceil.IsInt64() || !panicked && got != ceil.Int64() {
			t.Fatalf("%v.CeilAmount() = %d (panicked %v), want %s", e, got, panicked, ceil)
		}
	})
}

// TestExactRat pins the conversion to a fraction at the edges of the domain: one unit, the largest product, the sum of
// seven of them, and both ends of the 128-bit range, whose bits span both words. Billing multiplies the fraction by an
// exchange rate such as 10/71 and rounds up once to its smallest amount, 10⁻⁹; a value exactly on an amount stays
// there and one unit more rounds up, which only holds when the fraction is exact.
func TestExactRat(t *testing.T) {
	price := func(s string) UnitPrice {
		p, err := ParseUnitPrice(s)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	most := price("1000").Mul(1e10)
	seven := Exact{}
	for range 7 {
		var err error
		if seven, err = seven.Add(most); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		e      Exact
		units  string
		amount string // units × 10/71, rounded up to 10⁻⁹; empty to skip
	}{
		{Exact{}, "0", "0"},
		{price("0.000000000000001").Mul(1), "1", "1"},
		{price("0.000000071").Mul(1), "71000000", "10"},
		{price("0.000000071000001").Mul(1), "71000001", "11"},
		{most, "10000000000000000000000000000", "1408450704225352112677"},
		{seven, "70000000000000000000000000000", "9859154929577464788733"},
		{maxExact, "170141183460469231731687303715884105727", ""},
		{minExact, "-170141183460469231731687303715884105728", ""},
		{Exact{-1, 1<<64 - 1}, "-1", ""},
	} {
		n, _ := new(big.Int).SetString(c.units, 10)
		got := c.e.Rat()
		if want := new(big.Rat).SetFrac(n, big.NewInt(1e15)); got.Cmp(want) != 0 {
			t.Errorf("%v.Rat() = %s, want %s", c.e, got, want)
		}
		if c.amount == "" {
			continue
		}
		scaled := new(big.Rat).Mul(got, big.NewRat(10*1e9, 71))
		amount, rem := new(big.Int).QuoRem(scaled.Num(), scaled.Denom(), new(big.Int))
		if rem.Sign() > 0 {
			amount.Add(amount, big.NewInt(1))
		}
		if amount.String() != c.amount {
			t.Errorf("%s units × 10/71 rounds up to %s, want %s", c.units, amount, c.amount)
		}
	}
}
