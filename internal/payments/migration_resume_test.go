package payments

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/billing"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestReconcileMigrationsResumeWithoutLosingState(t *testing.T) {
	historical := []pg.Module{
		migrationsThrough(t, billing.Migrations(), "00024_payment_audit.sql"),
		migrationsThrough(t, Migrations(), "00006_reconcile.sql"),
	}
	// Module templates are cached by name, so a historical prefix needs an empty database of its own.
	database := pgtest.New(t)
	if err := pg.Migrate(t.Context(), database.Superuser, historical...); err != nil {
		t.Fatal(err)
	}
	w := newWorldOn(t, fakePlugin{create: goodCheckout}, database)
	if err := w.db.Check(t.Context(), historical...); err != nil {
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
	if err := pg.Migrate(t.Context(), w.super, historical...); err != nil {
		t.Fatal("resume concurrent builds", err)
	}
	if err := pg.Migrate(t.Context(), w.super, billing.Migrations(), Migrations()); err != nil {
		t.Fatal("upgrade after resuming concurrent builds", err)
	}
	if err := w.db.Check(t.Context(), billing.Migrations(), Migrations()); err != nil {
		t.Fatal("resumed schema rejected at startup", err)
	}
	if err := w.tx(w.p.platform, func(tx pg.Tx) error {
		h, err := queries.LockQueryHealth(t.Context(), tx, m.ID)
		if err != nil {
			return err
		}
		if diff := cmp.Diff(new(token), h.FirstToken); diff != "" {
			t.Fatal("migration reset the existing query lease (-want +got)", diff)
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

func migrationsThrough(t *testing.T, module pg.Module, last string) pg.Module {
	t.Helper()
	paths, err := fs.Glob(module.Migrations, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	prefix := fstest.MapFS{}
	for _, path := range paths {
		if path > last {
			continue
		}
		data, err := fs.ReadFile(module.Migrations, path)
		if err != nil {
			t.Fatal(err)
		}
		prefix[path] = &fstest.MapFile{Data: data}
	}
	module.Migrations = prefix
	return module
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
