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

func TestExchangeCreditsExactAndReciprocal(t *testing.T) {
	s := &Service{}
	schedule, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"XXX","rates":{"input":"0.000000000000001"}}`))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewV7()
	schedules := map[uuid.UUID]*catalog.Schedule{id: &schedule}
	s.schedules.Store(&schedules)
	value, _ := platform.ParseUnitPrice("71")
	rate := platform.ExchangeRate{ID: uuid.NewV7(), Base: "CNY", Quote: "USD", Numerator: 10, Denominator: 71, Version: 1}
	r := AttemptReceipt{schedule: id, currency: currency(t, "CNY"), unitValue: value, exchange: rate}
	// The review's double-rounding counterexample is scaled up by 10^6 so rounding to an amount reveals it.
	u, _ := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: 1000000}, catalog.ServiceTierStandard, nil)
	c, got, known, err := s.Credits(r, []catalog.Usage{u}, currency(t, "USD"))
	if err != nil || !known || got != 10 || c.Conversion != "converted" {
		t.Fatalf("one rounding: %d %v %+v %v", got, known, c, err)
	}
	forward, _ := rate.Between(currency(t, "CNY"), currency(t, "USD"))
	reverse, _ := rate.Between(currency(t, "USD"), currency(t, "CNY"))
	if new(big.Rat).Mul(forward, reverse).Cmp(big.NewRat(1, 1)) != 0 {
		t.Fatal("reciprocal lost precision")
	}
	rng := rand.New(rand.NewPCG(167, 71))
	for range 500 {
		n, d, count := rng.Int64N(1000000)+1, rng.Int64N(1000000)+1, rng.Int64N(10000000000)+1
		r.exchange.Numerator, r.exchange.Denominator = n, d
		u, _ := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemInput: count}, catalog.ServiceTierStandard, nil)
		_, got, known, err := s.Credits(r, []catalog.Usage{u}, currency(t, "USD"))
		// Independent integer quotient: count * 71 * n / (10^6 * d) billionths.
		num := new(big.Int).Mul(big.NewInt(count), big.NewInt(71*n))
		den := big.NewInt(1000000 * d)
		want, rem := new(big.Int), new(big.Int)
		want.QuoRem(num, den, rem)
		if rem.Sign() > 0 {
			want.Add(want, big.NewInt(1))
		}
		if err != nil || !known || got != want.Int64() {
			t.Fatalf("%d/%d count %d: %d want %s known %v err %v", n, d, count, got, want, known, err)
		}
	}
	// A malformed foreign rate cannot change same-currency arithmetic.
	r.exchange = platform.ExchangeRate{ID: uuid.NewV7(), Numerator: -1}
	_, with, _, _ := s.Credits(r, []catalog.Usage{u}, r.currency)
	r.exchange = platform.ExchangeRate{}
	_, without, _, _ := s.Credits(r, []catalog.Usage{u}, r.currency)
	if with != without {
		t.Fatal("same currency used exchange rate")
	}
}

func TestMissingExchangeDoesNotDisableNativeReconciliation(t *testing.T) {
	w := newWorld(t, 1)
	a := w.attempt(newRequest(), 1, "missing-exchange")
	a.Receipt.currency = currency(t, "CNY")
	a.Receipt.unitValue, _ = platform.ParseUnitPrice("2")
	c, amount, known, err := w.svc.Credits(a.Receipt, a.Usage, currency(t, "USD"))
	if err != nil || known || amount != 0 || c.Flag != CostComplete || c.Conversion != "missing_rate" {
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
	w.tx(func(ctx context.Context, tx pg.Tx) error {
		var unit, fx uuid.UUID
		var from, to, state, native, value string
		err := tx.QueryRow(ctx, `SELECT a.unit_value_id,a.exchange_rate_id,a.conversion_from,a.conversion_to,a.conversion_state,a.estimated_native::text,v.value::text
		FROM suppliers.attempt_costs a JOIN suppliers.unit_values v ON v.id=a.unit_value_id WHERE a.request_id=$1`, uuid.UUID(record.Request)).Scan(&unit, &fx, &from, &to, &state, &native, &value)
		if err != nil {
			return err
		}
		if unit != r.unitValueID || fx != r.exchange.ID || from != "CNY" || to != "USD" || state != "converted" {
			t.Errorf("basis changed: %s %s %s %s %s", unit, fx, from, to, state)
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
			t.Errorf("persisted basis gives %s, charged %d", q, amount)
		}
		return nil
	})
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
	r.unitValue, _ = platform.ParseUnitPrice("1000")
	u, _ := catalog.NewUsage(map[catalog.Item]int64{catalog.ItemOutput: 10000000000}, catalog.ServiceTierStandard, nil)
	c, n, known, err := w.svc.Credits(r, []catalog.Usage{u}, currency(t, "USD"))
	if err != nil || known || n != 0 || c.Conversion != "overflow" || c.Flag != CostComplete {
		t.Fatalf("overflow: %+v %d %v %v", c, n, known, err)
	}
	u, _ = catalog.NewUsage(map[catalog.Item]int64{catalog.ItemCacheRead: 1}, catalog.ServiceTierStandard, nil)
	c, _, known, err = w.svc.Credits(r, []catalog.Usage{u}, currency(t, "USD"))
	if err != nil || known || c.Flag != CostUnpriced || c.Conversion != "converted" {
		t.Fatalf("unpriced: %+v %v %v", c, known, err)
	}
}
