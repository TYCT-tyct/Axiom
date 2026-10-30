package billing

import (
	"errors"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/idem"
	"axiom/internal/platform/pg"
)

func fxInput(n, d, kind string, version int64) ExchangeInput {
	return ExchangeInput{Base: "USD", Quote: "CNY", Numerator: n, Denominator: d, Kind: kind, ExpectedVersion: version, Reason: "verified manual rate"}
}

func mustNormalize(t *testing.T, in ExchangeInput) platform.ExchangeRate {
	t.Helper()
	r, err := in.normalized()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// oneStale reports whether exactly one of two racing writes won and the other saw the superseded version.
func oneStale(a, b error) bool {
	return a == nil && errors.Is(b, ErrStaleExchange) || b == nil && errors.Is(a, ErrStaleExchange)
}

// race runs the writes at once, each in its own transaction, and returns their errors.
func race(t *testing.T, r rules, ins ...ExchangeInput) []error {
	start := make(chan struct{})
	done := make(chan error, len(ins))
	for _, in := range ins {
		go func() {
			<-start
			done <- r.tx(func(tx pg.Tx) error { _, err := SetExchangeRate(t.Context(), tx, system, in); return err })
		}()
	}
	close(start)
	errs := make([]error, len(ins))
	for i := range errs {
		errs[i] = <-done
	}
	return errs
}

func TestExchangeValidation(t *testing.T) {
	t.Parallel()

	for name, edit := range map[string]func(*ExchangeInput){
		"zero":                    func(in *ExchangeInput) { in.Numerator = "0" },
		"negative":                func(in *ExchangeInput) { in.Denominator = "-1" },
		"huge":                    func(in *ExchangeInput) { in.Numerator = strings.Repeat("9", 100000) },
		"component bound":         func(in *ExchangeInput) { in.Numerator = "1000000000000000001" },
		"unknown currency":        func(in *ExchangeInput) { in.Base = "ZZZ" },
		"no currency":             func(in *ExchangeInput) { in.Base = "XXX" },
		"same currency":           func(in *ExchangeInput) { in.Quote = "USD" },
		"range":                   func(in *ExchangeInput) { in.Numerator = "1000001"; in.Denominator = "1" },
		"empty correction reason": func(in *ExchangeInput) { in.Kind = "correction"; in.Reason = "  " },
	} {
		t.Run(name, func(t *testing.T) {
			in := fxInput("71", "10", "initial", 0)
			edit(&in)
			if _, err := in.normalized(); err == nil {
				t.Fatal("accepted invalid rate")
			}
		})
	}
	for _, in := range []ExchangeInput{fxInput("14", "100", "initial", 0), fxInput("1", "1000000", "initial", 0), fxInput("1000000", "1", "initial", 0)} {
		if _, err := in.normalized(); err != nil {
			t.Fatal(err)
		}
	}
	forward := mustNormalize(t, fxInput("71", "10", "initial", 0))
	in := fxInput("10", "71", "initial", 0)
	in.Base, in.Quote = "CNY", "USD"
	reverse := mustNormalize(t, in)
	if forward != reverse {
		t.Fatalf("directions differ: %+v %+v", forward, reverse)
	}
}

func TestExchangeChangeBounds(t *testing.T) {
	t.Parallel()

	previous := []BillingExchangeRate{{Version: 1, Numerator: 10, Denominator: 71}}
	for _, tc := range []struct {
		n, d, kind string
		want       bool
	}{
		{"355", "10", "adjustment", true},
		{"3551", "100", "adjustment", false},
		{"142", "100", "adjustment", true},
		{"1419", "1000", "adjustment", false},
		{"14", "100", "adjustment", false},
		{"14", "100", "correction", true},
		{"4000000", "1", "correction", false},
	} {
		in := fxInput(tc.n, tc.d, tc.kind, 1)
		next, err := in.normalized()
		if err == nil {
			err = checkExchangeChange(in, next, previous)
		}
		if (err == nil) != tc.want {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

func TestExchangeWritesAreSerializedAndAudited(t *testing.T) {
	t.Parallel()

	r := newRules(t)
	write := func(in ExchangeInput) (row BillingExchangeRate, err error) {
		err = r.tx(func(tx pg.Tx) error {
			var err error
			row, err = SetExchangeRate(t.Context(), tx, system, in)
			return err
		})
		return
	}
	if _, err := write(fxInput("71", "10", "initial", 0)); err != nil {
		t.Fatal(err)
	}
	errs := race(t, r, fxInput("284", "10", "adjustment", 1), fxInput("1775", "1000", "adjustment", 1))
	if !oneStale(errs[0], errs[1]) {
		t.Fatalf("concurrent results: %v", errs)
	}
	if err := r.tx(func(tx pg.Tx) error {
		rows, err := ExchangeRateHistory(t.Context(), tx, "USD", "CNY", 0)
		if err == nil && (len(rows) != 2 || rows[0].Version != 2 || rows[1].Version != 1) {
			t.Errorf("history: %+v", rows)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The row is appended before the invalid audit actor fails; the transaction must remove both.
	err := r.tx(func(tx pg.Tx) error {
		_, err := SetExchangeRate(t.Context(), tx, audit.Actor{}, fxInput("71", "10", "correction", 2))
		return err
	})
	if err == nil {
		t.Fatal("invalid audit actor accepted")
	}
	if row, err := write(fxInput("71", "10", "correction", 2)); err != nil || row.Version != 3 {
		t.Fatalf("rollback lost: %+v %v", row, err)
	}
}

func TestExchangeFirstDirectionsAndIdempotency(t *testing.T) {
	t.Parallel()

	r := newRules(t)
	// Each pair races once, as the overlap of two short transactions is not certain; CNY last, which the rest uses.
	for _, quote := range []string{"EUR", "GBP", "JPY", "HKD", "SGD", "AUD", "CAD", "CHF", "CNY"} {
		forward := fxInput("14", "100", "initial", 0)
		forward.Quote = quote
		reverse := fxInput("100", "14", "initial", 0)
		reverse.Base, reverse.Quote = quote, "USD"
		if errs := race(t, r, forward, reverse); !oneStale(errs[0], errs[1]) {
			t.Fatalf("first directions of %s: %v", quote, errs)
		}
	}
	in := fxInput("71", "10", "correction", 1)
	fingerprint, err := idem.Fingerprint(in)
	if err != nil {
		t.Fatal(err)
	}
	claim := idem.Claim{Key: uuid.NewV7(), Actor: system, Operation: "setExchangeRate", Fingerprint: fingerprint}
	once := func() (row BillingExchangeRate, replayed bool) {
		err := r.tx(func(tx pg.Tx) (err error) {
			row, replayed, err = idem.Once(t.Context(), tx, claim, func() (BillingExchangeRate, error) { return SetExchangeRate(t.Context(), tx, system, in) })
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return row, replayed
	}
	first, replayed := once()
	if replayed {
		t.Error("first call replayed")
	}
	// A newer version appears before the lost confirmation is retried; the retry must still return the first answer.
	if err := r.tx(func(tx pg.Tx) error {
		_, err := SetExchangeRate(t.Context(), tx, system, fxInput("72", "10", "adjustment", 2))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if row, replayed := once(); !replayed || row.ID != first.ID || row.Version != 2 {
		t.Errorf("replay changed basis: %+v replayed=%v", row, replayed)
	}
}

func TestExchangePlatformOnlyAndAppendOnly(t *testing.T) {
	t.Parallel()

	r := newRules(t)
	if err := r.tx(func(tx pg.Tx) error {
		_, err := SetExchangeRate(t.Context(), tx, system, fxInput("71", "10", "initial", 0))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []pg.Scope{pg.NewScope(false, nil, nil), pg.NewScope(false, nil, []uuid.UUID{uuid.NewV7()})} {
		if err := r.db.ReadTx(t.Context(), scope, func(tx pg.Tx) error {
			rows, err := ExchangeRates(t.Context(), tx)
			if len(rows) != 0 {
				t.Error("rate leaked")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := r.db.Tx(t.Context(), scope, func(tx pg.Tx) error {
			_, err := SetExchangeRate(t.Context(), tx, system, fxInput("71", "10", "initial", 0))
			return err
		}); err == nil {
			t.Error("unscoped write accepted")
		}
	}
	for _, sql := range []string{"UPDATE billing.exchange_rates SET numerator=1", "DELETE FROM billing.exchange_rates"} {
		if err := r.tx(func(tx pg.Tx) error { _, err := tx.Exec(t.Context(), sql); return err }); pg.SQLState(err) != "42501" {
			t.Errorf("history mutation: %v", err)
		}
	}
}

func TestFrozenRatesAndUnknownProfit(t *testing.T) {
	t.Parallel()

	old := platform.ExchangeRate{ID: uuid.NewV7(), Base: "CNY", Quote: "USD", Numerator: 10, Denominator: 71, Version: 1}
	missing := platform.ExchangeRate{Base: "JPY", Quote: "USD"}
	q := []Quotable{{Exchange: old}, {Exchange: old}, {Exchange: missing}, {}}
	frozen := frozenRates(q, []int{0, 1, 2, 3})
	q[0].Exchange.Version = 2
	if len(frozen) != 2 || frozen[0] != old || frozen[1] != missing {
		t.Fatalf("frozen: %+v", frozen)
	}
	c := locked{charged: 100, cost: platform.AmountExact(30), costKnown: true}
	if got := c.profit(); got == nil || *got != 70 {
		t.Fatalf("known profit: %v", got)
	}
	c.costKnown = false
	if c.profit() != nil {
		t.Fatal("unknown cost displayed as zero")
	}
}

func TestExchangeVersionWinsOverTimestamp(t *testing.T) {
	t.Parallel()

	r := newRules(t)
	if err := r.tx(func(tx pg.Tx) error {
		for version := int64(1); version <= 2; version++ {
			_, err := tx.Exec(t.Context(), `INSERT INTO billing.exchange_rates
			(id,base,quote,version,numerator,denominator,kind,reason,created_by,created_at)
			VALUES ($1,'CNY','USD',$2,$2,7,'correction','test equal timestamps','test','2000-01-01')`, uuid.NewV7(), version)
			if err != nil {
				return err
			}
		}
		rows, err := ExchangeRates(t.Context(), tx)
		if err == nil && (len(rows) != 1 || rows[0].Version != 2 || rows[0].Numerator != 2) {
			t.Errorf("current version: %+v", rows)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
