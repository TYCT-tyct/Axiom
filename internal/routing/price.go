package routing

import (
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"axiom/internal/platform"
)

// The price limits a request's provider.max_price and a preset's max_price set are read by the same rules, so a preset
// and a request mean the same price by the same digits (docs/domains/routing.md「价格上限」).

// priceNumber is a plain JSON number of at most a three-digit exponent: digits, an optional fraction, an optional
// exponent. Nothing else is a price, and the exponent's size is bounded here, before any arithmetic.
var priceNumber = regexp.MustCompile(`^([0-9]+)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]{1,3}))?$`)

// PricePerMillion turns a JSON number of credits per million tokens into the price of one token. The number is read as exact
// decimal digits and a power of ten, never through a float, so the limit is exactly what was written. A unit price
// counts 10⁻¹⁵ of a credit and a million tokens make 10⁻⁶ of that, so the price is the number times 10⁹ units, which
// has to be a whole number and at most 10¹⁸ (UnitPrice's limit). The digits and the power are checked to be within
// those bounds before the big integer is made, so what an exponent such as 1e1000000 asks for is never computed.
func PricePerMillion(number []byte) (platform.UnitPrice, bool) {
	units, ok := readPrice(number, 9)
	if !ok {
		return platform.UnitPrice{}, false
	}
	return units.price()
}

// PricePerRequest turns a JSON number of credits per request into the price of one request: no million-token scaling, so a
// number is multiplied by 10¹⁵ units, and its 15 decimals are all the unit price has.
func PricePerRequest(number []byte) (platform.UnitPrice, bool) {
	units, ok := readPrice(number, 15)
	if !ok {
		return platform.UnitPrice{}, false
	}
	return units.price()
}

// priceUnits is a number within the bounds of a unit price: its significant decimal digits and the power of ten they
// are multiplied by. Only readPrice makes one, so the big integer in price is reached only by a number whose digits
// and exponent already fit, and a number beyond them costs no more than the string work of the check.
type priceUnits struct {
	digits string
	shift  int
}

// readPrice reads a JSON number as the price units it asks for, or reports that it is not one. It is string work only:
// a huge exponent or a long run of digits is refused here, before any big-integer arithmetic.
func readPrice(number []byte, extra int) (priceUnits, bool) {
	m := priceNumber.FindSubmatch(number)
	if m == nil {
		return priceUnits{}, false
	}
	exp := 0
	if len(m[3]) > 0 {
		exp, _ = strconv.Atoi(string(m[3])) //nolint:errcheck // at most a sign and three digits
	}
	digits := strings.TrimLeft(string(m[1])+string(m[2]), "0")
	scale := exp - len(m[2]) // the number is digits × 10^scale
	if trimmed := strings.TrimRight(digits, "0"); digits != "" {
		scale += len(digits) - len(trimmed)
		digits = trimmed
	}
	if digits == "" {
		return priceUnits{}, true // the zero value is the price of nothing, whatever the exponent says
	}
	shift := scale + extra
	if len(digits) > 19 || shift < 0 || shift+len(digits) > 19 {
		return priceUnits{}, false
	}
	return priceUnits{digits: digits, shift: shift}, true
}

// price is the unit price u stands for.
func (u priceUnits) price() (platform.UnitPrice, bool) {
	if u.digits == "" {
		return platform.UnitPrice{}, true // the zero value is the price of nothing
	}
	units, _ := new(big.Int).SetString(u.digits, 10)
	units.Mul(units, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(u.shift)), nil))
	whole, frac := new(big.Int).QuoRem(units, big.NewInt(1e15), new(big.Int))
	fraction := frac.String()
	price, err := platform.ParseUnitPrice(whole.String() + "." + strings.Repeat("0", 15-len(fraction)) + fraction)
	return price, err == nil
}
