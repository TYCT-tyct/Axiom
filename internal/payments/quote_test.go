package payments

import (
	"errors"
	"math"
	"math/big"
	"math/rand/v2"
	"testing"

	"axiom/internal/platform"
)

func currency(t testing.TB, code string) platform.Currency {
	t.Helper()
	c, err := platform.ParseCurrency(code)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const yuan = int64(creditScale) // one unit of the site's currency in credits

func TestQuoteWorksTheFeeBack(t *testing.T) {
	for name, c := range map[string]struct {
		credits   int64
		applied   *big.Rat
		currency  string
		fee       Fee
		total, of int64
	}{
		// docs/domains/payments.md「渠道手续费」: 100 at 3% is 103.10, so the channel's 3.09 leaves at least 100.
		"the documented example": {100 * yuan, nil, "CNY", Fee{PPM: 30_000}, 10310, 310},
		"no fee":                 {100 * yuan, nil, "CNY", Fee{}, 10000, 0},
		"fixed fee":              {100 * yuan, nil, "CNY", Fee{PPM: 30_000, Fixed: 30}, 10341, 341},
		"one billionth":          {1, nil, "USD", Fee{}, 1, 0},
		"no minor unit":          {1000 * yuan, nil, "JPY", Fee{PPM: 36_000}, 1038, 38},
		"three digits":           {yuan, nil, "KWD", Fee{}, 1000, 0},
		// 10 USD at 7.2 with a 2% buffer is 73.44 CNY.
		"another currency": {10 * yuan, big.NewRat(72*102, 1000), "CNY", Fee{}, 7344, 0},
		"rounded up once":  {10 * yuan, big.NewRat(1, 3), "CNY", Fee{PPM: 10_000}, 337, 3},
	} {
		q, err := quote(c.credits, c.applied, currency(t, c.currency), c.fee)
		if err != nil || q.Total.Minor != c.total || q.Fee != c.of || q.Total.Currency.String() != c.currency {
			t.Errorf("%s: quote = %+v, %v; want total %d, fee %d", name, q, err, c.total, c.of)
		}
	}
}

func TestQuoteRefusesWhatDoesNotFit(t *testing.T) {
	usd, kwd := currency(t, "USD"), currency(t, "KWD")
	for name, c := range map[string]struct {
		credits  int64
		applied  *big.Rat
		currency platform.Currency
		fee      Fee
	}{
		"fee above 50%":                        {yuan, nil, usd, Fee{PPM: maxFeePPM + 1}},
		"negative fee":                         {yuan, nil, usd, Fee{PPM: -1}},
		"negative fixed fee":                   {yuan, nil, usd, Fee{Fixed: -1}},
		"no credits":                           {0, nil, usd, Fee{}},
		"credits × rate beyond int64":          {math.MaxInt64, big.NewRat(1_100_000, 1), kwd, Fee{}},
		"the total beyond int64":               {math.MaxInt64, big.NewRat(600_000, 1), kwd, Fee{PPM: maxFeePPM}},
		"the fixed fee pushes it beyond int64": {yuan, nil, usd, Fee{Fixed: math.MaxInt64}},
	} {
		if q, err := quote(c.credits, c.applied, c.currency, c.fee); !errors.Is(err, ErrOutOfRange) {
			t.Errorf("%s: quote = %+v, %v; want ErrOutOfRange", name, q, err)
		}
	}
	if _, err := quote(yuan, nil, usd, Fee{PPM: maxFeePPM}); err != nil {
		t.Errorf("a 50%% fee: %v", err)
	}
}

// TestQuoteProperty checks random quotes against an integer computation of the same formula and the properties the
// channel's cut relies on: the total is the least whose remainder after the fee covers the top-up and fixed fee.
func TestQuoteProperty(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // G404: test data, not a secret
	codes := []string{"USD", "CNY", "JPY", "KWD"}
	for range 5000 {
		credits := 1 + r.Int64N(1_000_000*yuan)
		num, den := 1+r.Int64N(1_000_000_000_000_000_000), 1+r.Int64N(1_000_000_000_000_000_000)
		if new(big.Rat).SetFrac64(num, den).Cmp(big.NewRat(1_000_000, 1)) > 0 || new(big.Rat).SetFrac64(num, den).Cmp(big.NewRat(1, 1_000_000)) < 0 {
			num, den = den%1000+1, den%997+1
		}
		fee := Fee{PPM: r.Int32N(maxFeePPM + 1), Fixed: r.Int64N(10_000)}
		c := currency(t, codes[r.IntN(len(codes))])
		q, err := quote(credits, big.NewRat(num, den), c, fee)
		if errors.Is(err, ErrOutOfRange) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		// need = (credits × num × 10^d ÷ (den × 10⁹) + fixed) × 10⁶, compared with total × (10⁶ − ppm), all over
		// den × 10⁹ so everything is an integer.
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(c.MinorDigits())), nil)
		under := new(big.Int).Mul(big.NewInt(den), big.NewInt(creditScale))
		need := new(big.Int).Mul(new(big.Int).Mul(big.NewInt(credits), big.NewInt(num)), scale)
		need.Add(need, new(big.Int).Mul(big.NewInt(fee.Fixed), under))
		need.Mul(need, big.NewInt(ppmScale))
		got := func(total int64) *big.Int {
			return new(big.Int).Mul(new(big.Int).Mul(big.NewInt(total), big.NewInt(ppmScale-int64(fee.PPM))), under)
		}
		if got(q.Total.Minor).Cmp(need) < 0 || got(q.Total.Minor-1).Cmp(need) >= 0 {
			t.Fatalf("quote(%d, %d/%d, %s, %+v) = %d, not the least total covering the top-up", credits, num, den, c, fee, q.Total.Minor)
		}
		part := ceil(new(big.Rat).SetFrac(new(big.Int).Mul(new(big.Int).Mul(big.NewInt(credits), big.NewInt(num)), scale), under))
		if q.Fee != q.Total.Minor-part.Int64() || q.Fee < fee.Fixed {
			t.Fatalf("quote(%d, %d/%d, %s, %+v) fee %d, want total − ⌈top-up⌉ = %d", credits, num, den, c, fee, q.Fee, q.Total.Minor-part.Int64())
		}
	}
}
