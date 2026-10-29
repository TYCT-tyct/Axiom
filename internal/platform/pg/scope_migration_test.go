package pg_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestScopeMigrationReleasesLocksAndResumes exercises the production migration across a later table's lock timeout.
// It must leave earlier tables usable and tolerate functions and policies committed by the interrupted run.
func TestScopeMigrationReleasesLocksAndResumes(t *testing.T) {
	database := pgtest.New(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if err := pgtest.Run(ctx, database.Superuser,
		"DELETE FROM platform.goose_platform WHERE version_id = 14",
		"ALTER POLICY tenant ON platform.audit_log USING (platform.in_scope(site_id, account_id))"); err != nil {
		t.Fatal(err)
	}
	holder, err := pgx.Connect(ctx, database.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close(context.WithoutCancel(ctx)) //nolint:errcheck // the held transaction is rolled back on disconnect
	if _, err := holder.Exec(ctx, "BEGIN; LOCK TABLE platform.tenants IN ACCESS SHARE MODE"); err != nil {
		t.Fatal(err)
	}
	watchCtx, stopWatching := context.WithCancel(ctx)
	observed := watchRunning(watchCtx, database.Superuser, "%ALTER POLICY reads ON platform.tenants%")
	defer func() {
		stopWatching()
		<-observed // receive the poll result even if requireSeen failed
	}()
	done := make(chan error, 1)
	go func() { done <- pg.Migrate(ctx, database.Migrator) }()
	requireSeen(t, observed, done)
	if err := pgtest.Run(ctx, database.Superuser,
		"BEGIN", "LOCK TABLE platform.audit_log IN ACCESS SHARE MODE NOWAIT", "ROLLBACK"); err != nil {
		t.Errorf("the earlier table is blocked while the later table waits: %v", err)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "SQLSTATE 55P03") {
		t.Fatalf("the blocked migration: %v, want lock_timeout", err)
	}
	var rewritten bool
	db := open(t, database.App)
	if err := db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
		return tx.QueryRow(ctx, `SELECT qual NOT LIKE '%in_scope%' FROM pg_policies
			WHERE schemaname = 'platform' AND tablename = 'audit_log' AND policyname = 'tenant'`).Scan(&rewritten)
	}); err != nil || !rewritten {
		t.Fatalf("the completed prefix was not preserved: %v, rewritten %v", err, rewritten)
	}
	if _, err := holder.Exec(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := pg.Migrate(ctx, database.Migrator); err != nil {
		t.Fatalf("retry the partially committed migration: %v", err)
	}
	if err := db.Check(ctx); err != nil {
		t.Errorf("audit after retry: %v", err)
	}
}
