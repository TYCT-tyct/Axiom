package payments

import (
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

// OrderID is an order's ID; its TypeID is the merchant order number sent to the channel.
type OrderID uuid.UUID

// TypeID writes the ID as the API and the channel see it.
func (id OrderID) TypeID() string { return orderPrefix.Encode(uuid.UUID(id)) }

// RefundID is a refund's ID, the refund number of channels that take one.
type RefundID uuid.UUID

// TypeID writes the ID as the API and the channel see it.
func (id RefundID) TypeID() string { return refundPrefix.Encode(uuid.UUID(id)) }

// Money is an amount the user pays, in minor units of its currency: never a float, compared with ==.
type Money struct {
	Currency platform.Currency
	Minor    int64
}

// minorDigits are the ISO 4217 currencies whose minor unit is not two digits, after sub2api's table
// (backend/internal/payment/currency.go:24-49 at 8f2cbb5) without its Stripe-only exceptions (ISK and UGX, which
// ISO 4217 gives no minor unit).
var minorDigits = map[string]int{
	"BIF": 0, "CLP": 0, "DJF": 0, "GNF": 0, "ISK": 0, "JPY": 0, "KMF": 0, "KRW": 0, "MGA": 0, "PYG": 0, "RWF": 0,
	"UGX": 0, "VND": 0, "VUV": 0, "XAF": 0, "XOF": 0, "XPF": 0,
	"BHD": 3, "IQD": 3, "JOD": 3, "KWD": 3, "LYD": 3, "OMR": 3, "TND": 3,
}

// digits is how many decimal digits currency's minor unit has.
func digits(currency platform.Currency) int {
	if d, ok := minorDigits[currency.String()]; ok {
		return d
	}
	return 2
}

// ParseOrderID reads an order's ID as the API writes it; a malformed one is ErrNotFound, like an unknown one.
func ParseOrderID(s string) (OrderID, error) {
	id, err := orderPrefix.Decode(s)
	if err != nil {
		return OrderID{}, ErrNotFound
	}
	return OrderID(id), nil
}
