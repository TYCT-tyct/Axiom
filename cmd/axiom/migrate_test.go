package main

import (
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/pgtest"
)

// TestMigrateBringsWhatServeChecks: a node refuses a database that has platform's migrations but not the modules',
// and starts once migrate has run, so both go through the one module list. Migrate grants roles cluster-wide, so
// this test does not run in parallel.
func TestMigrateBringsWhatServeChecks(t *testing.T) {
	database := pgtest.New(t)
	if _, err := openDatabase(t.Context(), database.App, 10*time.Second); err == nil ||
		!strings.Contains(err.Error(), "accounts (run axiom migrate first)") {
		t.Fatalf("before migrating: %v, want the accounts migrations missing", err)
	}
	if err := migrate(t.Context(), []string{"AXIOM_MIGRATE_DATABASE_URL=" + database.Migrator}); err != nil {
		t.Fatal(err)
	}
	db, err := openDatabase(t.Context(), database.App, 10*time.Second)
	if err != nil {
		t.Fatalf("after migrating: %v", err)
	}
	db.Close()
}
