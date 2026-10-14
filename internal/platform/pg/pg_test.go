package pg_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"uuid"

	"github.com/jackc/pgx/v5"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// testSchema is a table of account data under row-level security: forced, so its owner is filtered too, with one
// scope check for reading and writing. Its schema is created by the administrator, as migrate creates every schema.
const testSchema = `
CREATE SCHEMA rlstest AUTHORIZATION axiom_owner;
SET ROLE axiom_owner;
CREATE TABLE rlstest.items (site_id uuid NOT NULL, account_id uuid NOT NULL, note text NOT NULL);
ALTER TABLE rlstest.items ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON rlstest.items USING (platform.in_scope(site_id, account_id));
GRANT USAGE ON SCHEMA rlstest TO axiom_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON rlstest.items TO axiom_app;
RESET ROLE;`

// newDatabase returns a fresh database holding the protected test table.
func newDatabase(t *testing.T) pgtest.Database {
	t.Helper()
	db := pgtest.New(t)
	if err := pgtest.Run(t.Context(), db.Superuser, testSchema); err != nil {
		t.Fatal(err)
	}
	return db
}

// open connects as the application with a pool of one connection, so consecutive transactions share it.
func open(t *testing.T, url string) *pg.DB {
	t.Helper()
	db, err := pg.Open(t.Context(), url+"&pool_max_conns=1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

var (
	site1, site2                           = uuid.NewV7(), uuid.NewV7()
	account1, account2, account3, account4 = uuid.NewV7(), uuid.NewV7(), uuid.NewV7(), uuid.NewV7()
)

// seed writes one row for each of four accounts, two on each site, bypassing the application.
func seed(t *testing.T, db pgtest.Database) {
	t.Helper()
	rows := [][2]uuid.UUID{{site1, account1}, {site1, account2}, {site2, account3}, {site2, account4}}
	var values []string
	for _, row := range rows {
		values = append(values, fmt.Sprintf("('%s', '%s', 'seed')", row[0], row[1]))
	}
	if err := pgtest.Run(t.Context(), db.Superuser, "INSERT INTO rlstest.items VALUES "+strings.Join(values, ", ")); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, db *pg.DB, scope pg.Scope) int {
	t.Helper()
	var n int
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT count(*) FROM rlstest.items").Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestScopeIsASet: the scope is the union of every row, some sites' rows and some accounts' rows; owning a whole
// site and one account of another site never widens to that other site's remaining accounts.
func TestScopeIsASet(t *testing.T) {
	t.Parallel()
	database := newDatabase(t)
	seed(t, database)
	db := open(t, database.App)
	cases := []struct {
		name  string
		scope pg.Scope
		want  int
	}{
		{"platform", pg.NewScope(true, nil, nil), 4},
		{"one site", pg.NewScope(false, []uuid.UUID{site1}, nil), 2},
		{"one account", pg.NewScope(false, nil, []uuid.UUID{account3}), 1},
		{"a site and an account of another site", pg.NewScope(false, []uuid.UUID{site1}, []uuid.UUID{account3}), 3},
		{"two accounts", pg.NewScope(false, nil, []uuid.UUID{account2, account4}), 2},
	}
	for _, c := range cases {
		if got := count(t, db, c.scope); got != c.want {
			t.Errorf("%s: %d rows, want %d", c.name, got, c.want)
		}
	}
}

// TestNoScopeNoRows: without a scope nothing can be read, written, changed or deleted.
func TestNoScopeNoRows(t *testing.T) {
	t.Parallel()
	database := newDatabase(t)
	seed(t, database)
	db := open(t, database.App)
	if n := count(t, db, pg.Scope{}); n != 0 {
		t.Errorf("read %d rows without a scope", n)
	}
	for _, statement := range []string{
		"UPDATE rlstest.items SET note = 'changed' RETURNING note",
		"DELETE FROM rlstest.items RETURNING note",
	} {
		err := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
			var note string
			return tx.QueryRow(t.Context(), statement).Scan(&note)
		})
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("%s without a scope: %v, want no rows", statement, err)
		}
	}
	insert := func(scope pg.Scope, account uuid.UUID) error {
		return db.Tx(t.Context(), scope, func(tx pg.Tx) error {
			var note string
			return tx.QueryRow(t.Context(), "INSERT INTO rlstest.items VALUES ($1, $2, 'new') RETURNING note",
				site1, account).Scan(&note)
		})
	}
	if err := insert(pg.Scope{}, account1); err == nil {
		t.Error("inserted a row without a scope")
	}
	if err := insert(pg.NewScope(false, nil, []uuid.UUID{account2}), account1); err == nil {
		t.Error("inserted a row for an account outside the scope")
	}
	if err := insert(pg.NewScope(false, nil, []uuid.UUID{account1}), account1); err != nil {
		t.Errorf("inserting a row in scope: %v", err)
	}
}

// TestScopeEndsWithTransaction: on the one pooled connection, a scope set by one transaction is gone once it ends,
// whether it committed, failed or panicked: the connection itself carries no scope, and the next transaction without
// a scope reads nothing. A malformed scope fails rather than widens.
func TestScopeEndsWithTransaction(t *testing.T) {
	t.Parallel()
	database := newDatabase(t)
	seed(t, database)
	db := open(t, database.App)
	platform := pg.NewScope(true, nil, nil)
	failed := errors.New("failed")
	endings := map[string]func(){
		"commit": func() { count(t, db, platform) },
		"error": func() {
			if err := db.Tx(t.Context(), platform, func(pg.Tx) error { return failed }); !errors.Is(err, failed) {
				t.Errorf("the transaction's error was lost: %v", err)
			}
		},
		"panic": func() {
			defer func() {
				if recover() == nil {
					t.Error("the handler's panic did not reach the caller")
				}
			}()
			err := db.Tx(t.Context(), platform, func(pg.Tx) error { panic("handler bug") })
			t.Errorf("Tx returned %v instead of panicking", err)
		},
	}
	for name, end := range endings {
		end()
		if left, err := db.SessionScope(t.Context()); err != nil || left != "" {
			t.Errorf("after a transaction that ended by %s, the connection carries the scope %q (%v)", name, left, err)
		}
		if n := count(t, db, pg.Scope{}); n != 0 {
			t.Errorf("after a transaction that ended by %s, the next one without a scope read %d rows", name, n)
		}
	}
	err := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM (SELECT set_config('axiom.scope_sites', 'everything', true))").Scan(&n); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), "SELECT count(*) FROM rlstest.items").Scan(&n)
	})
	if err == nil {
		t.Error("a malformed scope did not fail the query")
	}
}

// TestRowLevelSecurityBindsEveryone: the table's owner is filtered too (FORCE), and Check refuses a login that can
// escape row-level security or a database that misses migrations.
func TestRowLevelSecurityBindsEveryone(t *testing.T) {
	t.Parallel()
	database := newDatabase(t)
	seed(t, database)
	var owned int
	conn, err := pgx.Connect(t.Context(), database.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(t.Context()) //nolint:errcheck // the test is over
	if err := pgx.BeginFunc(t.Context(), conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), "SET LOCAL ROLE axiom_owner"); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), "SELECT count(*) FROM rlstest.items").Scan(&owned)
	}); err != nil {
		t.Fatal(err)
	}
	if owned != 0 {
		t.Errorf("the table's owner read %d rows without a scope", owned)
	}

	if err := open(t, database.App).Check(t.Context()); err != nil {
		t.Errorf("Check refused the application role: %v", err)
	}
	if err := open(t, database.Superuser).Check(t.Context()); err == nil || !strings.Contains(err.Error(), "bypass") {
		t.Errorf("Check accepted a superuser: %v", err)
	}
	if err := pgtest.Run(t.Context(), database.Superuser, "DELETE FROM platform.goose_platform WHERE version_id > 0"); err != nil {
		t.Fatal(err)
	}
	if err := open(t, database.App).Check(t.Context()); err == nil || !strings.Contains(err.Error(), "axiom migrate") {
		t.Errorf("Check accepted a database without its migrations: %v", err)
	}
}
