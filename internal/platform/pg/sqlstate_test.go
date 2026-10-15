package pg_test

import (
	"errors"
	"fmt"
	"testing"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestSQLState: a PostgreSQL error, wrapped as callers wrap it, gives its SQLSTATE, and any other error none.
func TestSQLState(t *testing.T) {
	t.Parallel()
	db := open(t, pgtest.New(t).App)
	err := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT 1/0").Scan(new(int))
	})
	if got := pg.SQLState(fmt.Errorf("divide: %w", err)); got != "22012" { // division_by_zero
		t.Errorf("SQLState of a division by zero is %q", got)
	}
	if got := pg.SQLState(errors.New("not from the database")); got != "" {
		t.Errorf("SQLState of another error is %q", got)
	}
}
