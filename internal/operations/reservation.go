package operations

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/platform/pg"
)

// Reservation is a money operation's reservation as it is now. Expires is nil for one that ends with its money's
// outcome, not at a time.
type Reservation struct {
	Operation uuid.UUID
	Category  Category
	Object    Target
	Amount    int64
	Currency  string
	Basis     []byte
	State     string
	Expires   *time.Time
	Proof     string
	Settled   *time.Time
}

// ReservationOf reads operation id's reservation; found is false when it moves no money.
func ReservationOf(ctx context.Context, tx pg.Tx, id uuid.UUID) (Reservation, bool, error) {
	rows, err := New().MoneyActionOf(ctx, tx, id)
	if err != nil || len(rows) == 0 {
		return Reservation{}, false, err
	}
	return reservationOf(rows[0]), true, nil
}

func reservationOf(m OperationsMoneyAction) Reservation {
	r := Reservation{
		Operation: m.OperationID, Category: Category(m.Category), Object: Target{Kind: m.ObjectKind, ID: m.ObjectID},
		Amount: m.Amount, Currency: m.Currency, Basis: m.Basis, State: m.State, Proof: deref(m.Proof), Settled: m.SettledAt,
	}
	if !m.ExpiresAt.Equal(untilOutcome) {
		r.Expires = &m.ExpiresAt
	}
	return r
}
