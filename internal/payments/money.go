package payments

import (
	"errors"
	"math/big"
	"strconv"
	"strings"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/typeid"
)

// Each kind of ID is a type of its own, written behind its TypeID prefix at the HTTP boundary (README.md「接口」).
var (
	merchantPrefix = typeid.MustPrefix("mch")
	orderPrefix    = typeid.MustPrefix("ord")
	refundPrefix   = typeid.MustPrefix("rfd")
)

// MerchantID is a merchant account's ID.
type MerchantID uuid.UUID

// TypeID writes the ID as the API shows it.
func (id MerchantID) TypeID() string { return merchantPrefix.Encode(uuid.UUID(id)) }

// ParseMerchantID reads a merchant account's ID as the API writes it; a malformed one is ErrNotFound, like an unknown one.
func ParseMerchantID(s string) (MerchantID, error) {
	id, err := merchantPrefix.Decode(s)
	if err != nil {
		return MerchantID{}, ErrNotFound
	}
	return MerchantID(id), nil
}

// OrderID is an order's ID; its TypeID is the merchant order number sent to the channel.
type OrderID uuid.UUID

// TypeID writes the ID as the API and the channel see it.
func (id OrderID) TypeID() string { return orderPrefix.Encode(uuid.UUID(id)) }

// RefundID is a refund's ID, the refund number of channels that take one.
type RefundID uuid.UUID

// TypeID writes the ID as the API and the channel see it.
func (id RefundID) TypeID() string { return refundPrefix.Encode(uuid.UUID(id)) }

// ParseRefundID reads a refund's ID as the API writes it; a malformed one is ErrNotFound, like an unknown one.
func ParseRefundID(s string) (RefundID, error) {
	id, err := refundPrefix.Decode(s)
	if err != nil {
		return RefundID{}, ErrNotFound
	}
	return RefundID(id), nil
}

// Money is an amount the user pays, in minor units of its currency: never a float, compared with ==.
type Money struct {
	Currency platform.Currency
	Minor    int64
}

// AmountFromMinor writes minor units as the admin API's exact decimal amount, in billionths of one unit
// (api/admin.yaml Money). It is text because a JSON number would round an amount above 2^53 in the browser.
// AmountFromMinor writes minor units as the contract's amount: an integer count of billionths of one unit.
func AmountFromMinor(currency platform.Currency, minor int64) string {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(9-currency.MinorDigits())), nil)
	return new(big.Int).Mul(big.NewInt(minor), scale).String()
}

// MinorFromAmount reads the contract's amount, an integer count of billionths, as minor units; false when it is not a
// whole minor unit of the currency or does not fit int64.
func MinorFromAmount(currency platform.Currency, amount string) (int64, bool) {
	n, ok := new(big.Int).SetString(amount, 10)
	if !ok || n.Sign() < 0 {
		return 0, false
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(9-currency.MinorDigits())), nil)
	q, r := new(big.Int).QuoRem(n, scale, new(big.Int))
	if r.Sign() != 0 || !q.IsInt64() {
		return 0, false
	}
	return q.Int64(), true
}

// ParseOrderID reads an order's ID as the API writes it; a malformed one is ErrNotFound, like an unknown one.
func ParseOrderID(s string) (OrderID, error) {
	id, err := orderPrefix.Decode(s)
	if err != nil {
		return OrderID{}, ErrNotFound
	}
	return OrderID(id), nil
}

// parseMinorText reads a channel's decimal amount as minor units: 0 for a missing amount, -1 for one that is
// unrepresentable, which the core treats as a verified but invalid payment for review.
func parseMinorText(text string, places int) int64 {
	if text == "" {
		return 0
	}
	whole, fraction, err := decimalParts(text)
	if err != nil {
		return -1
	}
	fraction = strings.TrimRight(fraction, "0")
	if len(fraction) > places {
		return -1
	}
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	// Bound before concatenating or parsing a gateway-controlled integer.
	if len(whole) > 19 {
		return -1
	}
	minorText := strings.TrimLeft(whole+fraction+strings.Repeat("0", places-len(fraction)), "0")
	if minorText == "" {
		return -1
	}
	if len(minorText) > 19 || (len(minorText) == 19 && minorText > "9223372036854775807") {
		return -1
	}
	minor, err := strconv.ParseInt(minorText, 10, 64)
	if err != nil {
		return -1
	}
	return minor
}

func decimalParts(text string) (string, string, error) {
	whole, fraction, hasDot := strings.Cut(text, ".")
	if whole == "" || strings.Trim(whole, "0123456789") != "" || (hasDot && (fraction == "" || strings.Trim(fraction, "0123456789") != "")) {
		return "", "", errors.New("payments: invalid decimal amount")
	}
	return whole, fraction, nil
}

// formatMinorText writes minor units as the decimal text channels take, without a float.
func formatMinorText(m Money) string {
	text := strconv.FormatInt(m.Minor, 10)
	places := m.Currency.MinorDigits()
	if places == 0 {
		return text
	}
	if len(text) <= places {
		text = strings.Repeat("0", places+1-len(text)) + text
	}
	return text[:len(text)-places] + "." + text[len(text)-places:]
}
