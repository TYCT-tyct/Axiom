package platform

import (
	"errors"
	"strings"
)

// Currency is a three-letter currency code in upper case, such as USD or CNY.
type Currency struct{ code string }

// ParseCurrency accepts exactly three ASCII upper-case letters.
func ParseCurrency(s string) (Currency, error) {
	if len(s) != 3 || strings.Trim(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return Currency{}, errors.New("currency: want three upper-case letters such as USD")
	}
	return Currency{s}, nil
}

func (c Currency) String() string { return c.code }
