package suppliers

import (
	"context"
	"math/big"
	"math/rand/v2"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
)

func unitPrice(t *testing.T, s string) platform.UnitPrice {
	t.Helper()
	v, err := platform.ParseUnitPrice(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func usageFor(t *testing.T, item catalog.Item, n int64) catalog.Usage {
	t.Helper()
	u, err := catalog.NewUsage(map[catalog.Item]int64{item: n}, catalog.ServiceTierStandard, nil)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func creditsOf(t *testing.T, s *Service, r AttemptReceipt, u catalog.Usage, to string) (AttemptCost, int64, bool) {
	t.Helper()
	c, amount, known, err := s.Credits(r, []catalog.Usage{u}, currency(t, to))
	if err != nil {
		t.Fatal(err)
	}
	return c, amount, known
}

// wantQuotient is the independent integer quotient count * 71 * n / (10^6 * d) billionths, rounded up.
func wantQuotient(count, n, d int64) int64 {
	num := new(big.Int).Mul(big.NewInt(count), big.NewInt(71*n))
	want, rem := new(big.Int), new(big.Int)
	want.QuoRem(num, big.NewInt(1000000*d), rem)
	if rem.Sign() > 0 {
		want.Add(want, big.NewInt(1))
	}
	return want.Int64()
}

func TestExchangeCreditsExactAndReciprocal(t *testing.T) {
	s := &Service{}
	schedule, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"XXX","rates":{"input":"0.000000000000001"}}`))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewV7()
	schedules := map[uuid.UUID]*catalog.Schedule{id: &schedule}
	s.schedules.Store(&schedules)
	value := unitPrice(t, "71")
	rate := platform.ExchangeRate{ID: uuid.NewV7(), Base: "CNY", Quote: "USD", Numerator: 10, Denominator: 71, Version: 1}
	r := AttemptReceipt{schedule: id, currency: currency(t, "CNY"), unitValue: value, exchange: rate}
	// The review's double-rounding counterexample is scaled up by 10^6 so rounding to an amount reveals it.
	u := usageFor(t, catalog.ItemInput, 1000000)
	c, got, known := creditsOf(t, s, r, u, "USD")
	if !known || got != 10 || c.Conversion != ConversionConverted {
		t.Fatalf("one rounding: %d %v %+v", got, known, c)
	}
	forward, _ := rate.Between(currency(t, "CNY"), currency(t, "USD"))
	reverse, _ := rate.Between(currency(t, "USD"), currency(t, "CNY"))
	if new(big.Rat).Mul(forward, reverse).Cmp(big.NewRat(1, 1)) != 0 {
		t.Fatal("reciprocal lost precision")
	}
	rng := rand.New(rand.NewPCG(167, 71)) //nolint:gosec // G404: test data, not a secret
	for range 500 {
		n, d, count := rng.Int64N(1000000)+1, rng.Int64N(1000000)+1, rng.Int64N(10000000000)+1
		r.exchange.Numerator, r.exchange.Denominator = n, d
		_, got, known := creditsOf(t, s, r, usageFor(t, catalog.ItemInput, count), "USD")
		if want := wantQuotient(count, n, d); !known || got != want {
			t.Fatalf("%d/%d count %d: %d want %d known %v", n, d, count, got, want, known)
		}
	}
	// A malformed foreign rate cannot change same-currency arithmetic.
	r.exchange = platform.ExchangeRate{ID: uuid.NewV7(), Numerator: -1}
	_, with, _ := creditsOf(t, s, r, u, "CNY")
	r.exchange = platform.ExchangeRate{}
	_, without, _ := creditsOf(t, s, r, u, "CNY")
	if with != without {
		t.Fatal("same currency used exchange rate")
	}
}

func TestMissingExchangeDoesNotDisableNativeReconciliation(t *testing.T) {
	w := newWorld(t, 1)
	a := w.attempt(newRequest(), 1, "missing-exchange")
	a.Receipt.currency = currency(t, "CNY")
	a.Receipt.unitValue = unitPrice(t, "2")
	c, amount, known, err := w.svc.Credits(a.Receipt, a.Usage, currency(t, "USD"))
	if err != nil || known || amount != 0 || c.Flag != CostComplete || c.Conversion != ConversionMissingRate {
		t.Fatalf("missing: %+v %d %v %v", c, amount, known, err)
	}
	a.Cost = c
	w.record(a)
	w.ingest(w.charge("missing-exchange", "45", w.clock.now()))
	report := w.reconcile(w.clock.now())
	if len(report.Delisted) != 1 {
		t.Fatalf("native underestimation skipped: %+v", report)
	}
}

// checkPersistedBasis recomputes the charged amount from the three persisted factors alone.
func (w *world) checkPersistedBasis(request platform.RequestID, r AttemptReceipt, amount int64) {
	w.t.Helper()
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		var unit, fx uuid.UUID
		var from, to, state, native, value string
		err := tx.QueryRow(ctx, `SELECT a.unit_value_id,a.exchange_rate_id,a.conversion_from,a.conversion_to,a.conversion_state,a.estimated_native::text,v.value::text
		FROM suppliers.attempt_costs a JOIN suppliers.unit_values v ON v.id=a.unit_value_id WHERE a.request_id=$1`, uuid.UUID(request)).Scan(&unit, &fx, &from, &to, &state, &native, &value)
		if err != nil {
			return err
		}
		if unit != r.unitValueID || fx != r.exchange.ID || from != "CNY" || to != "USD" || state != "converted" {
			w.t.Errorf("basis changed: %s %s %s %s %s", unit, fx, from, to, state)
		}
		n, _ := new(big.Rat).SetString(native)
		v, _ := new(big.Rat).SetString(value)
		n.Mul(n, v).Mul(n, big.NewRat(10, 71)).Mul(n, big.NewRat(1e9, 1))
		q, rem := new(big.Int), new(big.Int)
		q.QuoRem(n.Num(), n.Denom(), rem)
		if rem.Sign() != 0 {
			q.Add(q, big.NewInt(1))
		}
		if q.Int64() != amount {
			w.t.Errorf("persisted basis gives %s, charged %d", q, amount)
		}
		return nil
	})
}

func TestExchangeBasisPersistsAndNeverChanges(t *testing.T) {
	w := newWorld(t, 1)
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	r := a.Done()
	r.currency = currency(t, "CNY")
	r.exchange = platform.ExchangeRate{ID: uuid.NewV7(), Base: "CNY", Quote: "USD", Numerator: 10, Denominator: 71, Version: 1}
	usage := usageOf100x50(t)
	c, amount, known, err := w.svc.Credits(r, usage, currency(t, "USD"))
	if err != nil || !known {
		t.Fatal(err)
	}
	record := AttemptRecord{Request: newRequest(), Seq: 1, Receipt: r, Usage: usage, Cost: c, FinishedAt: w.clock.now()}
	w.record(record)
	w.addValue("3")
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	record.Receipt.exchange.ID = uuid.NewV7()
	record.Receipt.unitValueID = uuid.NewV7()
	w.record(record)
	w.checkPersistedBasis(record.Request, r, amount)
	// Legacy writers omit all basis columns; no current value is used to fill them.
	legacy := w.attempt(newRequest(), 1, "")
	w.record(legacy)
	if w.count("SELECT count(*) FROM suppliers.attempt_costs WHERE request_id=$1 AND unit_value_id IS NULL AND exchange_rate_id IS NULL AND conversion_state IS NULL", uuid.UUID(legacy.Request)) != 1 {
		t.Fatal("legacy basis was invented")
	}
}

func TestExchangeOverflowAndUnpricedStayUnknown(t *testing.T) {
	w := newWorld(t, 1)
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	r := a.Done()
	r.currency = currency(t, "CNY")
	r.exchange = platform.ExchangeRate{ID: uuid.NewV7(), Base: "CNY", Quote: "USD", Numerator: 1000000, Denominator: 1}
	r.unitValue = unitPrice(t, "1000")
	c, n, known := creditsOf(t, w.svc, r, usageFor(t, catalog.ItemOutput, 10000000000), "USD")
	if known || n != 0 || c.Conversion != ConversionOverflow || c.Flag != CostComplete {
		t.Fatalf("overflow: %+v %d %v", c, n, known)
	}
	c, _, known = creditsOf(t, w.svc, r, usageFor(t, catalog.ItemCacheRead, 1), "USD")
	if known || c.Flag != CostUnpriced || c.Conversion != ConversionConverted {
		t.Fatalf("unpriced: %+v %v", c, known)
	}
}

// Every conversion the code names passes the table's check (which repeats the names in SQL), and only one that reached
// the multiplication keeps the rate's ID: an attempt that did not run, or had no rate, was converted with none.
func TestConversionStatesAreStoredAndKeepTheRateOnlyWhenUsed(t *testing.T) {
	w := newWorld(t, 1)
	rate := platform.ExchangeRate{ID: uuid.NewV7(), Base: "CNY", Quote: "USD", Numerator: 10, Denominator: 71, Version: 1}
	for c := ConversionSame; c <= ConversionNotRun; c++ {
		a := w.attempt(newRequest(), 1, "")
		a.Receipt.currency, a.Receipt.exchange = currency(t, "CNY"), rate
		a.Cost.Currency, a.Cost.Conversion = currency(t, "USD"), c
		w.record(a)
		used := c == ConversionConverted || c == ConversionOverflow
		if w.count(`SELECT count(*) FROM suppliers.attempt_costs
			WHERE request_id=$1 AND conversion_state=$2 AND (exchange_rate_id IS NOT NULL) = $3`, uuid.UUID(a.Request), c.String(), used) != 1 {
			t.Errorf("%s: not stored as itself, or the rate was kept %v", c, !used)
		}
	}
}
