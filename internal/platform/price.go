// Package platform holds the value types that every module shares and that the lowest module, catalog, already needs:
// unit prices, the exact amounts computed from them, and currencies. It has types and arithmetic only, no business
// rules; how prices are rounded and converted between currencies is billing's (docs/domains/billing.md「钱的表示」).
package platform

import (
	"cmp"
	"errors"
	"math"
	"math/big"
	"math/bits"
	"strconv"
	"strings"
)

const (
	priceDecimals = 15
	priceScale    = 1e15
	maxUnitPrice  = 1000 * priceScale
)

// UnitPrice is a price per billing unit (a token, a request) in units of 10⁻¹⁵ of a currency, from 0 to 1000. An
// entered price is made only from decimal text, so it is exactly what an operator or an official page wrote; a price
// computed from a fraction rounds up instead (docs/domains/billing.md「钱的表示」); that constructor does not exist yet.
type UnitPrice struct{ n int64 }

var errUnitPrice = errors.New("unit price: want a decimal from 0 to 1000 with at most 15 decimals, such as 0.000003")

// ParseUnitPrice reads plain decimal text: digits, optionally a point and 1 to 15 more digits. A sign, an exponent or
// anything else is rejected, so text never means something other than what it shows.
func ParseUnitPrice(s string) (UnitPrice, error) {
	whole, frac, point := strings.Cut(s, ".")
	if !isDigits(whole) || point && (!isDigits(frac) || len(frac) > priceDecimals) {
		return UnitPrice{}, errUnitPrice
	}
	var n uint64
	for i := range len(whole) {
		// Stopping as soon as the whole part passes the bound keeps a long run of digits from overflowing.
		if n = n*10 + uint64(whole[i]-'0'); n > maxUnitPrice/priceScale {
			return UnitPrice{}, errUnitPrice
		}
	}
	n *= priceScale
	scale := uint64(priceScale)
	for i := range len(frac) {
		scale /= 10
		n += uint64(frac[i]-'0') * scale
	}
	if n > maxUnitPrice {
		return UnitPrice{}, errUnitPrice
	}
	return UnitPrice{int64(n)}, nil
}

func isDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// String is the canonical text of the price: no leading zeros, no trailing decimal zeros. Stored price schedules are
// compared byte for byte, so equal prices must always print alike.
func (p UnitPrice) String() string {
	whole := strconv.FormatInt(p.n/priceScale, 10)
	frac := p.n % priceScale
	if frac == 0 {
		return whole
	}
	digits := strconv.FormatInt(priceScale+frac, 10)[1:] // the leading 1 keeps the zeros after the point
	return whole + "." + strings.TrimRight(digits, "0")
}

// Cmp returns -1, 0 or +1 as p is less than, equal to or greater than q.
func (p UnitPrice) Cmp(q UnitPrice) int { return cmp.Compare(p.n, q.n) }

// Mul returns the exact cost of count billing units. It cannot overflow: the price is below 2⁶⁰ and the count below
// 2⁶³, so the product is below 2¹²³. A negative count is a bug in the caller, which validates usage first.
func (p UnitPrice) Mul(count int64) Exact {
	if count < 0 {
		panic("platform: UnitPrice.Mul with a negative count")
	}
	hi, lo := bits.Mul64(uint64(p.n), uint64(count)) //nolint:gosec // neither is negative
	return Exact{int64(hi), lo}                      //nolint:gosec // hi < 2⁵⁹, see above
}

// Exact is a signed 128-bit amount in units of 10⁻¹⁵ of a currency, the same units as UnitPrice; the currency travels
// with the price schedule that produced it. The value is hi × 2⁶⁴ + lo.
type Exact struct {
	hi int64
	lo uint64
}

var errOverflow = errors.New("exact amount: the sum overflows 128 bits")

// Add returns e + f. Within the documented domain of prices and usage a sum cannot overflow; it is still checked
// rather than wrapped, so an error is the worst outcome of a broken bound.
func (e Exact) Add(f Exact) (Exact, error) {
	lo, carry := bits.Add64(e.lo, f.lo, 0)
	uhi, _ := bits.Add64(uint64(e.hi), uint64(f.hi), carry) //nolint:gosec // two's complement: the bits are the value
	hi := int64(uhi)                                        //nolint:gosec // as above
	// Two's complement addition overflows exactly when both operands have the same sign and the sum the other one.
	if e.hi < 0 == (f.hi < 0) && hi < 0 != (e.hi < 0) {
		return Exact{}, errOverflow
	}
	return Exact{hi, lo}, nil
}

// Neg returns -e. Only the least value has no negation; amounts are far from it, so it panics rather than wrap.
func (e Exact) Neg() Exact {
	if e.hi == math.MinInt64 && e.lo == 0 {
		panic("platform: the negation of the least exact amount")
	}
	lo, borrow := bits.Sub64(0, e.lo, 0)
	hi, _ := bits.Sub64(0, uint64(e.hi), borrow) //nolint:gosec // two's complement: the bits are the value
	return Exact{int64(hi), lo}                  //nolint:gosec // as above
}

// Cmp returns -1, 0 or +1 as e is less than, equal to or greater than f. The high words carry the sign, the low words
// are unsigned.
func (e Exact) Cmp(f Exact) int {
	if c := cmp.Compare(e.hi, f.hi); c != 0 {
		return c
	}
	return cmp.Compare(e.lo, f.lo)
}

// Rat returns the amount as an exact fraction of a currency unit. Exchange rates and markups such as 10/71 have no
// finite decimal form, so billing multiplies this fraction and rounds once at the end.
func (e Exact) Rat() *big.Rat { return new(big.Rat).SetFrac(e.int(), big.NewInt(priceScale)) }

func (e Exact) int() *big.Int {
	n := new(big.Int).Lsh(big.NewInt(e.hi), 64)
	return n.Add(n, new(big.Int).SetUint64(e.lo))
}

// amountScale is the units of Exact in the smallest stored amount, a billionth of a currency unit
// (docs/domains/billing.md「钱的表示」).
const amountScale = 1e6

// AmountExact is n billionths of a currency unit, exactly. It cannot overflow: |n| × 10⁶ < 2⁸³.
func AmountExact(n int64) Exact {
	hi, lo := bits.Mul64(uint64(n), amountScale) //nolint:gosec // two's complement: the magnitude is fixed up below
	if n >= 0 {
		return Exact{int64(hi), lo} //nolint:gosec // hi < 2²⁰
	}
	// uint64(n) is n + 2⁶⁴, so the product is n × 10⁶ + 10⁶ × 2⁶⁴: take 10⁶ off the high word.
	return Exact{int64(hi) - amountScale, lo} //nolint:gosec // hi < 10⁶, so the difference is negative and small
}

// CeilAmount is the amount in billionths of a currency unit, rounded up, as every amount charged or reserved is
// (docs/domains/billing.md「钱的表示」). An amount beyond int64 is a bug: prices and usage are bounded far below it,
// so it panics rather than wrap.
func (e Exact) CeilAmount() int64 {
	q, m := new(big.Int).DivMod(e.int(), big.NewInt(amountScale), new(big.Int)) // Euclidean: q is the floor
	if m.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		panic("platform: an exact amount beyond the stored range")
	}
	return q.Int64()
}
