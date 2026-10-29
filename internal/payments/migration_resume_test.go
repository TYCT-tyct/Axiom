package payments

import (
	"testing"

	"axiom/internal/billing"
	"axiom/internal/platform/pg"
)

func TestReconcileMigrationsResumeWithoutLosingState(t *testing.T) {
	w := newTestWorld(t, fakePlugin{create: goodCheckout})
	m := w.waiting()
	o := w.mustPlace(yuan, "migration-resume")
	w.receive(receipt(o))
	token, err := w.p.acquireQuery(t.Context(), o.MerchantID, false)
	if err != nil {
		t.Fatal(err)
	}
	// Leave all schema steps present, as a failure in the last concurrent build would, without an applied marker.
	w.exec("DELETE FROM platform.goose_payments WHERE version_id=5", "DELETE FROM platform.goose_billing WHERE version_id=22")
	if err := pg.Migrate(t.Context(), w.super, billing.Migrations(), Migrations()); err != nil {
		t.Fatal("resume concurrent builds", err)
	}
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		h, err := queries.LockQueryHealth(t.Context(), tx, m.ID)
		if err != nil {
			return err
		}
		if h.FirstToken == nil || *h.FirstToken != token {
			t.Fatal("migration reset the existing query lease")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w.assertCredit(o, o.Credits)
	if n := w.count(w.p.platform, `SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
 WHERE i.indisvalid AND c.relname IN ('discrepancies_by_id','orders_changed_by_merchant','orders_merchant_cursor','credit_batches_by_merchant_day','transactions_refunds_by_day')`); n != 5 {
		t.Fatal("resumed migration left invalid or missing daily indexes", n)
	}
	if err := w.p.finishQuery(t.Context(), MerchantID(m.ID), token, queryUnused); err != nil {
		t.Fatal(err)
	}
}
