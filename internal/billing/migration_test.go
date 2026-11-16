package billing

import (
	"testing"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

func TestBaselineMigrationReruns(t *testing.T) {
	database := pgtest.New(t, Migrations())
	if err := pg.Migrate(t.Context(), database.Migrator, Migrations()); err != nil {
		t.Fatal(err)
	}
	db, err := pg.Open(t.Context(), database.App)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Check(t.Context(), Migrations()); err != nil {
		t.Fatal(err)
	}
}
