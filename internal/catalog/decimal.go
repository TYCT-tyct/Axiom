package catalog

import (
	"fmt"
	"strconv"
	"strings"
)

// maxExponent bounds the exponent of a dataset number so that expanding it cannot build a huge string; a unit price has
// at most 15 decimals and 4 integer digits, so a larger exponent is not a price either.
const maxExponent = 30

// plainDecimal writes a dataset's number, such as 3e-06, 1.25E-7 or 0.000003, as plain decimal text with no exponent
// and no trailing zeros, the form platform.ParseUnitPrice reads. It works on the digits as written and never converts
// to a float, so the price is exactly what the dataset says; a number with more than 15 decimals is refused, not rounded
// (README.md「参考价的录入」).
func plainDecimal(s string) (string, error) {
	digits, point, err := splitNumber(s)
	if err != nil {
		return "", err
	}
	switch {
	case point <= 0:
		digits = "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		digits += strings.Repeat("0", point-len(digits))
	default:
		digits = digits[:point] + "." + digits[point:]
	}
	if strings.Contains(digits, ".") {
		digits = strings.TrimSuffix(strings.TrimRight(digits, "0"), ".")
	}
	if _, frac, _ := strings.Cut(digits, "."); len(frac) > 15 {
		return "", fmt.Errorf("number %q: more than 15 decimals", s)
	}
	if digits = strings.TrimLeft(digits, "0"); digits == "" || digits[0] == '.' {
		digits = "0" + digits
	}
	return digits, nil
}

// splitNumber reads digits[.digits][e[+-]digits] and returns all the digits with the position of the decimal point in
// them, which can lie outside them.
func splitNumber(s string) (digits string, point int, err error) {
	mantissa, exp, hasExp := strings.Cut(strings.ToLower(s), "e")
	exponent := 0
	if hasExp {
		if exponent, err = strconv.Atoi(exp); err != nil || exponent < -maxExponent || exponent > maxExponent {
			return "", 0, fmt.Errorf("number %q: the exponent is not an integer from -%d to %d", s, maxExponent, maxExponent)
		}
	}
	whole, frac, hasPoint := strings.Cut(mantissa, ".")
	digits = whole + frac
	if whole == "" || hasPoint && frac == "" || strings.Trim(digits, "0123456789") != "" {
		return "", 0, fmt.Errorf("number %q: want digits with an optional point and exponent, not a sign", s)
	}
	return digits, len(whole) + exponent, nil
}
