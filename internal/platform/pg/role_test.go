package pg_test

import (
	"errors"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// TestSetRoleEndsWithTransaction: SetRole runs the rest of a transaction as a role the login is a member of, still
// bound by the transaction's scope; on the one pooled connection the next transaction runs as the login again,
// whether the first committed or failed. A role the login is not a member of fails the transaction.
func TestSetRoleEndsWithTransaction(t *testing.T) {
	t.Parallel()
	database := newDatabase(t)
	seed(t, database)
	ctx := t.Context()
	// Roles belong to the cluster every test shares, so the name is the test's own.
	job := "job_" + strings.ReplaceAll(uuid.NewV7().String(), "-", "")
	if err := pgtest.Run(ctx, database.Superuser, "CREATE ROLE "+job, "GRANT "+job+" TO axiom_app",
		"GRANT USAGE ON SCHEMA rlstest TO "+job, "GRANT SELECT ON rlstest.items TO "+job); err != nil {
		t.Fatal(err)
	}
	db := open(t, database.App)
	failed := errors.New("failed")
	for _, ending := range []error{nil, failed} {
		var user string
		var n int
		err := db.Tx(ctx, tenants["a site"], func(tx pg.Tx) error {
			if err := tx.SetRole(ctx, job); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, "SELECT current_user, count(*) FROM rlstest.items").Scan(&user, &n); err != nil {
				return err
			}
			return ending
		})
		if !errors.Is(err, ending) || user != job || n != 2 {
			t.Errorf("ending with %v: %v, ran as %s and read %d rows, want %s and site1's 2", ending, err, user, n, job)
		}
		if after := currentUser(t, db); after != "axiom_app" {
			t.Errorf("after a transaction that switched roles and ended with %v, the next one runs as %s", ending, after)
		}
	}
	if err := db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error { return tx.SetRole(ctx, "axiom_owner") }); err == nil {
		t.Error("switched to a role the login is not a member of")
	}
}

func currentUser(t *testing.T, db *pg.DB) string {
	t.Helper()
	var user string
	if err := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT current_user").Scan(&user)
	}); err != nil {
		t.Fatal(err)
	}
	return user
}
