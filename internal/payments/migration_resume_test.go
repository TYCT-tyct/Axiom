package payments

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"axiom/internal/billing"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestReconcileMigrationsResumeWithoutLosingState(t *testing.T) {
	previousBilling := migrationsThrough(t, billing.Migrations(), "00024_payment_audit.sql")
	previousPayments := migrationsThrough(t, Migrations(), "00006_reconcile.sql")
	// pgtest caches templates by module name, so migrate the historical sets on their own database.
	d := pgtest.New(t)
	if err := pg.Migrate(t.Context(), d.Migrator, previousBilling, previousPayments); err != nil {
		t.Fatal("prepare historical schema", err)
	}
	w := newTestWorldOn(t, fakePlugin{create: goodCheckout}, d)
	if err := w.db.Check(t.Context(), previousBilling, previousPayments); err != nil {
		t.Fatal("migrated schema rejected at startup", err)
	}
	m := w.waiting()
	o := w.mustPlace(yuan, "migration-resume")
	w.receive(receipt(o))
	token, err := w.p.acquireQuery(t.Context(), o.MerchantID, false)
	if err != nil {
		t.Fatal(err)
	}
	// Leave all schema steps present, as a failure in the last concurrent build would, without an applied marker.
	w.exec("DELETE FROM platform.goose_payments WHERE version_id=6", "DELETE FROM platform.goose_billing WHERE version_id=24")
	if err := pg.Migrate(t.Context(), w.super, billing.Migrations(), Migrations()); err != nil {
		t.Fatal("resume concurrent builds", err)
	}
	if err := w.db.Check(t.Context(), billing.Migrations(), Migrations()); err != nil {
		t.Fatal("resumed schema rejected at startup", err)
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

func TestQueryHealthKeepsSiteScope(t *testing.T) {
	w := newWorld(t)
	m := w.waiting()
	token, err := w.p.acquireQuery(t.Context(), MerchantID(m.ID), false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := w.p.finishQuery(t.Context(), MerchantID(m.ID), token, queryUnused); err != nil {
			t.Error(err)
		}
	}()
	for _, test := range []struct {
		name  string
		scope pg.Scope
		want  int64
	}{
		{"platform", w.p.platform, 1},
		{"own site", siteScope(w.siteA), 1},
		{"other site", siteScope(w.siteB), 0},
		{"own account", w.accountA(), 0},
		{"other account", w.accountB(), 0},
		{"empty", pg.Scope{}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if n := w.count(test.scope, "SELECT count(*) FROM payments.query_health"); int64(n) != test.want {
				t.Fatalf("query health read scope: got %d, want %d", n, test.want)
			}
			if err := w.tx(test.scope, func(tx pg.Tx) error {
				tag, err := tx.Exec(t.Context(), "UPDATE payments.query_health SET failures=failures")
				if err == nil && tag.RowsAffected() != test.want {
					t.Fatalf("query health write scope: got %d, want %d", tag.RowsAffected(), test.want)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func migrationsThrough(t *testing.T, module pg.Module, last string) pg.Module {
	t.Helper()
	files, err := fs.Glob(module.Migrations, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	older := fstest.MapFS{}
	for _, name := range files {
		if name > last {
			continue
		}
		data, err := fs.ReadFile(module.Migrations, name)
		if err != nil {
			t.Fatal(err)
		}
		older[name] = &fstest.MapFile{Data: data}
	}
	module.Migrations = older
	return module
}
