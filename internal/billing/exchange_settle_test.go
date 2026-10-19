package billing

import (
	"testing"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

func TestExchangeReservationPersistsBeforeAnyAttempt(t *testing.T) {
	b := newBank(t, 10000, 0, Config{})
	q := metered(t)
	q.Exchange = platform.ExchangeRate{ID: uuid.NewV7(), Base: "CNY", Quote: "USD", Version: 1, Numerator: 10, Denominator: 71}
	h := b.holdOn(q, 100)
	// A process lost before its first attempt still leaves its reservation's exact basis behind.
	if err := b.app.ReadTx(t.Context(), b.admitted.Scope, func(tx pg.Tx) error {
		var raw []byte
		if err := tx.QueryRow(t.Context(), "SELECT exchange_rates FROM billing.reservations WHERE request_id=$1", h.id).Scan(&raw); err != nil {
			return err
		}
		var rates []platform.ExchangeRate
		if err := jsonx.Unmarshal(raw, &rates); err != nil {
			return err
		}
		if len(rates) != 1 || rates[0] != q.Exchange {
			t.Errorf("stored basis: %+v", rates)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Cleanup cannot invent the attempts or cost; its profit remains unknown.
	b.sql("UPDATE billing.reservations SET deadline='2000-01-01' WHERE request_id='" + h.id.String() + "'")
	if _, err := b.billing.Cleanup(t.Context(), 10); err != nil {
		t.Fatal(err)
	}
	if n := b.count("SELECT count(*) FROM billing.reservations WHERE profit IS NULL AND exchange_rates IS NOT NULL"); n != 1 {
		t.Fatal("cleanup filled a missing cost basis")
	}
}

func TestExchangeProfitUnknownAndSettlementReplay(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "one unknown attempt", true: "all known"}[known], func(t *testing.T) {
			b := newBank(t, 10000, 0, Config{})
			h := b.hold(100)
			o := outcome(Committed, FinishStop, used(t, 50, 20, 1))
			o.Attempts[0].Seq = 2
			o.Attempts[0].CostKnown = true
			failed := AttemptResult{Seq: 1, Cost: 5, CostKnown: known, Result: Unknown}
			o.Attempts = append([]AttemptResult{failed}, o.Attempts...)
			first, err := b.billing.Settle(t.Context(), h, o)
			if err != nil {
				t.Fatal(err)
			}
			if (first.Profit != nil) != known {
				t.Fatalf("profit known=%v: %v", known, first.Profit)
			}
			if known && *first.Profit != 68 {
				t.Fatalf("profit %d want 80 - 5 - 7", *first.Profit)
			}
			// A retry carrying changed costs must return the originally persisted result.
			o.Attempts[0].CostKnown = !known
			o.Attempts[0].Cost = 1000
			again, err := b.billing.Settle(t.Context(), h, o)
			if err != nil {
				t.Fatal(err)
			}
			if (again.Profit != nil) != known || known && *again.Profit != *first.Profit {
				t.Fatalf("replay changed profit: %v", again.Profit)
			}
		})
	}
}
