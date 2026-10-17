package suppliers

import (
	"context"
	"errors"
	"testing"
	"uuid"

	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestCreditsPricesAtTheUnitValue: the native cost times the unit value the attempt began with, in billionths, rounded
// up once; known only when it is the whole cost in the currency asked for.
func TestCreditsPricesAtTheUnitValue(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	receipt := a.Done()
	tiered := receipt
	tiered.schedule = uuidOf(7)
	sched, err := catalog.ParseSchedule([]byte(`{"v":1,"currency":"XXX","rates":{"input":"0.075","output":"0.3"},
		"context_tiers":{"basis":["input"],"method":"whole","tiers":[{"above":1000,"rates":{"input":"0.15"}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	(*w.svc.schedules.Load())[tiered.schedule] = &sched
	at := func(r AttemptReceipt, value string) AttemptReceipt {
		v, err := platform.ParseUnitPrice(value)
		if err != nil {
			t.Fatal(err)
		}
		r.unitValue = v
		return r
	}
	usage := func(counts map[catalog.Item]int64) []catalog.Usage {
		u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
		if err != nil {
			t.Fatal(err)
		}
		return []catalog.Usage{u}
	}
	usd, cny := currency(t, "USD"), currency(t, "CNY")
	in, out := catalog.ItemInput, catalog.ItemOutput
	for _, tc := range []struct {
		name     string
		receipt  AttemptReceipt
		usage    []catalog.Usage
		currency platform.Currency
		want     int64
		known    bool
	}{
		{"22.5 quota at 2", receipt, usage(map[catalog.Item]int64{in: 100, out: 50}), usd, 45e9, true},
		{"the long-context tier", tiered, usage(map[catalog.Item]int64{in: 2000}), usd, 600e9, true},
		{"at the tier's threshold", tiered, usage(map[catalog.Item]int64{in: 1000}), usd, 150e9, true},
		{"the least unit value rounds up", at(receipt, "0.000000000000001"), usage(map[catalog.Item]int64{in: 1}), usd, 1, true},
		{"an exact billionth stays", at(receipt, "0.000000001"), usage(map[catalog.Item]int64{out: 10}), usd, 3, true},
		{"the most unit value", at(receipt, "1000"), usage(map[catalog.Item]int64{out: 1000}), usd, 300_000e9, true},
		{"beyond an amount", at(receipt, "1000"), usage(map[catalog.Item]int64{out: 1e10}), usd, 0, false},
		{"another currency", receipt, usage(map[catalog.Item]int64{in: 100}), cny, 0, false},
		{"an item with no price: the priced part", receipt, usage(map[catalog.Item]int64{in: 100, catalog.ItemCacheRead: 10}), usd, 15e9, false},
		{"no usage", receipt, nil, usd, 0, true},
		{"a receipt with no settlement currency", AttemptReceipt{}, usage(map[catalog.Item]int64{in: 100}), usd, 0, false},
	} {
		got, known, err := w.svc.Credits(tc.receipt, tc.usage, tc.currency)
		if err != nil || got != tc.want || known != tc.known {
			t.Errorf("%s: %d, %v, %v; want %d, %v", tc.name, got, known, err, tc.want, tc.known)
		}
	}
}

func currency(t *testing.T, code string) platform.Currency {
	t.Helper()
	c, err := platform.ParseCurrency(code)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestOnlySuppliersWithAUnitValueAreRouted: an attempt takes the latest unit value and records the row it came from,
// and keeps both through a reload; a supplier with none has no endpoint to call; a value with more decimals than a
// price holds is refused by the table, so no row can make a reload fail.
func TestOnlySuppliersWithAUnitValueAreRouted(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 2)
	value := func(v string) uuid.UUID {
		var id uuid.UUID
		w.tx(func(ctx context.Context, tx pg.Tx) error {
			return tx.QueryRow(ctx, "INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES ($1, $2::numeric, 'test', 'later') RETURNING id", w.supplier, v).Scan(&id)
		})
		return id
	}
	began, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	three := value("3")
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	later, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	before, after := began.Done(), later.Done()
	if got2, got3 := before.unitValue.String(), after.unitValue.String(); got2 != "2" || got3 != "3" {
		t.Errorf("unit values %s and %s, want 2 for the attempt begun before and 3 after", got2, got3)
	}
	if after.unitValueID != three || before.unitValueID == three {
		t.Errorf("unit value ids %s and %s, want the attempt after the reload to record %s", before.unitValueID, after.unitValueID, three)
	}
	err = w.db.Tx(t.Context(), platformScope, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "INSERT INTO suppliers.unit_values (supplier_id, value, actor, reason) VALUES ($1, 0.0000000000000001, 'test', 'bad')", w.supplier)
		return err
	})
	if err == nil {
		t.Error("a unit value of 16 decimals was stored")
	}
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Errorf("a reload after the refused write failed: %v", err)
	}
	if err := pgtest.Run(t.Context(), w.database.Superuser, "DELETE FROM suppliers.unit_values"); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoEndpoint) {
		t.Errorf("a supplier with no unit value: %v, want ErrNoEndpoint", err)
	}
}
