package payments

import (
	"fmt"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform/pg"
)

func TestDailyReconcileScopesHistoryAndBatches(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	m := w.waiting()
	old := w.mustPlace(yuan, "old-accounting-day")
	w.receive(receipt(old))
	w.exec("UPDATE payments.orders SET created_at='2000-01-01 12:00:00+00',updated_at='2000-01-01 12:00:00+00',paid_fee=paid_fee+1",
		"UPDATE billing.credit_batches SET created_at='2000-01-01 12:00:00+00'",
		"UPDATE payments.merchant_usage SET day='2000-01-01'",
		`INSERT INTO billing.credit_batches SELECT (jsonb_populate_record(NULL::billing.credit_batches,to_jsonb(b)||jsonb_build_object('id',gen_random_uuid()))).* FROM billing.credit_batches b CROSS JOIN generate_series(1,1000)`)
	fresh := w.mustPlace(yuan, "current-accounting-day")
	w.receive(receipt(fresh))
	day := w.accountingDay(fresh.ID)
	if err := w.p.reconcileMerchant(t.Context(), m.ID, day); err != nil {
		t.Fatal(err)
	}
	if len(w.pending(w.p.platform, uuid.UUID{}, "")) != 0 {
		t.Fatal("daily check scanned unrelated historical discrepancies")
	}
	// A change today must recheck the old order and its original usage day.
	w.exec(fmt.Sprintf("UPDATE payments.orders SET updated_at=(SELECT updated_at FROM payments.orders WHERE id='%s') WHERE id='%s'", uuid.UUID(fresh.ID), uuid.UUID(old.ID)))
	if err := w.p.reconcileMerchant(t.Context(), m.ID, day); err != nil {
		t.Fatal(err)
	}
	if len(w.pending(w.p.platform, uuid.UUID{}, "")) != 1 {
		t.Fatal("today's changed historical order was not checked")
	}
	w.exec("UPDATE payments.merchant_usage SET used=used+1 WHERE day='2000-01-01'")
	seedDayBatches(w, fresh)
	ids := w.dayBatches(m.ID, day)
	if len(ids) != 102 {
		t.Fatal("daily batch cursor included history or lost a same-timestamp page", len(ids))
	}
	if err := w.p.reconcileMerchant(t.Context(), m.ID, day); err != nil {
		t.Fatal(err)
	}
	if n := w.count(w.p.platform, "SELECT count(*) FROM payments.discrepancies"); n != len(ids)+1 {
		t.Fatal("daily bulk owner check lost orphaned batches or affected usage", n, len(ids)+1)
	}
}

func seedDayBatches(w *world, o Order) {
	w.t.Helper()
	w.exec(fmt.Sprintf(`INSERT INTO billing.credit_batches
 SELECT (jsonb_populate_record(NULL::billing.credit_batches,to_jsonb(b)||jsonb_build_object('id',gen_random_uuid()))).*
 FROM billing.credit_batches b CROSS JOIN generate_series(1,101) n WHERE b.id='%s'`, uuid.UUID(o.ID)))
}

func (w *world) accountingDay(id OrderID) time.Time {
	w.t.Helper()
	var day time.Time
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		return tx.QueryRow(w.t.Context(), "SELECT date_trunc('day',coalesce(b.created_at,o.created_at) AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' FROM payments.orders o LEFT JOIN billing.credit_batches b ON b.id=o.id WHERE o.id=$1", uuid.UUID(id)).Scan(&day)
	}); err != nil {
		w.t.Fatal(err)
	}
	return day
}

func (w *world) runDailyQueries(merchant uuid.UUID, day time.Time) error {
	var after uuid.UUID
	for {
		next, err := w.p.reconcileQueries(w.t.Context(), merchant, day, after)
		if err != nil || next == (uuid.UUID{}) {
			return err
		}
		after = next
	}
}

func (w *world) dayBatches(merchant uuid.UUID, day time.Time) []uuid.UUID {
	w.t.Helper()
	t := w.t
	var ids []uuid.UUID
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		var after uuid.UUID
		for {
			page, err := billing.MerchantBatches(t.Context(), tx, merchant, after, day, day.Add(24*time.Hour))
			if err != nil {
				return err
			}
			if len(page) == 0 {
				break
			}
			batches, err := billing.ReadCredits(t.Context(), tx, page)
			if err != nil {
				return err
			}
			if len(batches) != len(page) {
				t.Fatal("bulk read omitted a batch")
			}
			ids = append(ids, page...)
			after = page[len(page)-1]
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return ids
}
