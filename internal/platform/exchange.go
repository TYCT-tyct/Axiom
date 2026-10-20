package platform

import (
	"math/big"
	"uuid"
)

// ExchangeRate is an immutable conversion basis: one Base buys Numerator/Denominator Quote.
// A zero ID means the pair was missing when the request was reserved, never a rate of one.
type ExchangeRate struct {
	ID          uuid.UUID `json:"id"`
	Version     int64     `json:"version"`
	Base        string    `json:"base"`
	Quote       string    `json:"quote"`
	Numerator   int64     `json:"numerator"`
	Denominator int64     `json:"denominator"`
}

// Between keeps the reciprocal exact; callers choose rounding only after all factors are multiplied.
func (r ExchangeRate) Between(from, to Currency) (*big.Rat, bool) {
	if r.ID == (uuid.UUID{}) {
		return nil, false
	}
	switch {
	case from.String() == r.Base && to.String() == r.Quote:
		return big.NewRat(r.Numerator, r.Denominator), true
	case from.String() == r.Quote && to.String() == r.Base:
		return big.NewRat(r.Denominator, r.Numerator), true
	}
	return nil, false
}
