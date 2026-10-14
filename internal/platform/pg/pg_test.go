package pg_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"uuid"

	"github.com/google/go-cmp/cmp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
)

// testSchema has two tables under row-level security, forced so their owner is filtered too: items is account data,
// with one scope check for reading and writing; shared is public data, which any scope reads and only the platform
// scope writes. Their schema is created by the administrator, as migrate creates every schema.
const testSchema = `
CREATE SCHEMA rlstest AUTHORIZATION axiom_owner;
SET ROLE axiom_owner;
CREATE TABLE rlstest.items (site_id uuid NOT NULL, account_id uuid NOT NULL, note text NOT NULL);
ALTER TABLE rlstest.items ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant ON rlstest.items USING (platform.in_scope(site_id, account_id));
CREATE TABLE rlstest.shared (note text NOT NULL);
ALTER TABLE rlstest.shared ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
CREATE POLICY read ON rlstest.shared FOR SELECT USING (platform.in_any_scope());
CREATE POLICY write ON rlstest.shared USING (platform.in_platform_scope());
GRANT USAGE ON SCHEMA rlstest TO axiom_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON rlstest.items, rlstest.shared TO axiom_app;
RESET ROLE;`

// newDatabase returns a fresh database holding the protected test tables.
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

// TestOpenRefusesBadURLs: Open names the variable and the problem, and quotes nothing from the URL. A non-positive
// health check period is one pgx would panic on in the background.
func TestOpenRefusesBadURLs(t *testing.T) {
	t.Parallel()
	const planted = "postgres://planted-user@planted-host/planted-db"
	urls := map[string]string{
		planted + "?pool_health_check_period=0s":  "AXIOM_DATABASE_URL: pool_health_check_period",
		planted + "?pool_health_check_period=-1s": "AXIOM_DATABASE_URL: pool_health_check_period",
		planted + "%zz": "AXIOM_DATABASE_URL: not a PostgreSQL connection string",
	}
	for url, want := range urls {
		_, err := pg.Open(t.Context(), url)
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "planted") {
			t.Errorf("%s: %v", url, err)
		}
	}
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

// visible lists, sorted, the accounts whose rows the scope can read.
func visible(t *testing.T, db *pg.DB, scope pg.Scope) []string {
	t.Helper()
	var accounts string
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(),
			"SELECT coalesce(string_agg(account_id::text, ',' ORDER BY account_id::text), '') FROM rlstest.items").Scan(&accounts)
	}); err != nil {
		t.Fatal(err)
	}
	return strings.FieldsFunc(accounts, func(r rune) bool { return r == ',' })
}

func sorted(ids ...uuid.UUID) []string {
	texts := make([]string, len(ids))
	for i, id := range ids {
		texts[i] = id.String()
	}
	return slices.Sorted(slices.Values(texts))
}

// TestScopeIsASet: the scope is the union of every row, some sites' rows and some accounts' rows; owning a whole
// site and one account of another site sees exactly those rows, never that other site's remaining accounts.
func TestScopeIsASet(t *testing.T) {
	t.Parallel()
	database := newDatabase(t)
	seed(t, database)
	db := open(t, database.App)
	cases := []struct {
		name  string
		scope pg.Scope
		want  []string
	}{
		{"platform", pg.NewScope(true, nil, nil), sorted(account1, account2, account3, account4)},
		{"one site", pg.NewScope(false, []uuid.UUID{site1}, nil), sorted(account1, account2)},
		{"one account", pg.NewScope(false, nil, []uuid.UUID{account3}), sorted(account3)},
		{
			"a site and an account of another site",
			pg.NewScope(false, []uuid.UUID{site1}, []uuid.UUID{account3}),
			sorted(account1, account2, account3),
		},
		{"two accounts", pg.NewScope(false, nil, []uuid.UUID{account2, account4}), sorted(account2, account4)},
	}
	for _, c := range cases {
		if diff := cmp.Diff(c.want, visible(t, db, c.scope)); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.name, diff)
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
	moved := db.Tx(t.Context(), pg.NewScope(false, nil, []uuid.UUID{account1}), func(tx pg.Tx) error {
		var note string
		return tx.QueryRow(t.Context(), "UPDATE rlstest.items SET account_id = $1 WHERE account_id = $2 RETURNING note",
			account2, account1).Scan(&note)
	})
	if moved == nil {
		t.Error("moved a row out of the scope")
	}
	if got := visible(t, db, pg.NewScope(false, nil, []uuid.UUID{account1})); len(got) != 2 {
		t.Errorf("after the refused move, account1 has rows of %v, want its two", got)
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

// TestTxRollsBackWhenItsContextEnds: a transaction whose context ends before it commits leaves no writes behind.
func TestTxRollsBackWhenItsContextEnds(t *testing.T) {
	t.Parallel()
	database := newDatabase(t)
	seed(t, database)
	db, platform := open(t, database.App), pg.NewScope(true, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	if err := db.Tx(ctx, platform, func(tx pg.Tx) error {
		defer cancel()
		return tx.QueryRow(ctx, "WITH gone AS (DELETE FROM rlstest.items RETURNING 1) SELECT count(*) FROM gone").Scan(new(int))
	}); err == nil {
		t.Error("the transaction committed after its context ended")
	}
	if n := count(t, db, platform); n != 4 {
		t.Errorf("the cancelled transaction left %d of 4 rows", n)
	}
}

var tenants = map[string]pg.Scope{
	"a site":     pg.NewScope(false, []uuid.UUID{site1}, nil),
	"an account": pg.NewScope(false, nil, []uuid.UUID{account1}),
}

// publicData connects as the application to a fresh database whose public table holds one row, seed.
func publicData(t *testing.T) *pg.DB {
	t.Helper()
	database := newDatabase(t)
	if err := pgtest.Run(t.Context(), database.Superuser, "INSERT INTO rlstest.shared VALUES ('seed')"); err != nil {
		t.Fatal(err)
	}
	return open(t, database.App)
}

// TestPublicDataReads: any non-empty scope reads public data and no scope reads none; a malformed scope fails rather
// than counts as one.
func TestPublicDataReads(t *testing.T) {
	t.Parallel()
	db := publicData(t)
	seed := []string{"seed"}
	for name, read := range map[string]struct {
		scope pg.Scope
		want  []string
	}{
		"no scope":           {pg.Scope{}, []string{}},
		"a site's scope":     {tenants["a site"], seed},
		"an account's scope": {tenants["an account"], seed},
		"the platform scope": {pg.PlatformScope(), seed},
	} {
		if diff := cmp.Diff(read.want, sharedNotes(t, db, read.scope)); diff != "" {
			t.Errorf("read in %s (-want +got):\n%s", name, diff)
		}
	}
	malformed := "SELECT set_config('axiom.scope_sites', 'everything', true); SELECT count(*) FROM rlstest.shared"
	if _, err := exec(t, db, pg.Scope{}, malformed); err == nil {
		t.Error("a malformed scope did not fail the query")
	}
}

// TestPublicDataWrites: only the platform scope, which production code builds with PlatformScope, writes public
// data; in any other scope an insert fails and an update or a delete finds no row.
func TestPublicDataWrites(t *testing.T) {
	t.Parallel()
	db := publicData(t)
	writes := []string{
		"INSERT INTO rlstest.shared VALUES ('added')",
		"UPDATE rlstest.shared SET note = 'changed'",
		"DELETE FROM rlstest.shared WHERE note = 'changed'",
	}
	for name, scope := range tenants {
		for _, statement := range writes {
			if n, err := exec(t, db, scope, statement); err == nil && n > 0 {
				t.Errorf("%s in %s's scope changed %d rows", statement, name, n)
			}
		}
	}
	platform := pg.PlatformScope()
	if diff := cmp.Diff([]string{"seed"}, sharedNotes(t, db, platform)); diff != "" {
		t.Errorf("after the refused writes (-want +got):\n%s", diff)
	}
	for _, statement := range writes {
		if n, err := exec(t, db, platform, statement); err != nil || n == 0 {
			t.Errorf("%s in the platform scope: %d rows, %v", statement, n, err)
		}
	}
}

// sharedNotes lists, sorted, the public rows the scope can read.
func sharedNotes(t *testing.T, db *pg.DB, scope pg.Scope) []string {
	t.Helper()
	var notes []string
	if err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), "SELECT note FROM rlstest.shared ORDER BY note")
		if err != nil {
			return err
		}
		notes, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return notes
}

// exec runs statement in its own transaction and reports how many rows it touched.
func exec(t *testing.T, db *pg.DB, scope pg.Scope, statement string) (int64, error) {
	t.Helper()
	var tag pgconn.CommandTag
	err := db.Tx(t.Context(), scope, func(tx pg.Tx) error {
		var err error
		tag, err = tx.Exec(t.Context(), statement)
		return err
	})
	return tag.RowsAffected(), err
}

// TestReadTxSeesOneMoment: every query of a ReadTx sees the database as it was at the transaction's start, so a
// commit between two of its reads shows only in the next transaction; and a ReadTx cannot write.
func TestReadTxSeesOneMoment(t *testing.T) {
	t.Parallel()
	database := newDatabase(t)
	seed(t, database)
	db := open(t, database.App)
	platform := pg.NewScope(true, nil, nil)
	var first, second int
	err := db.ReadTx(t.Context(), platform, func(tx pg.Tx) error {
		if err := tx.QueryRow(t.Context(), "SELECT count(*) FROM rlstest.items").Scan(&first); err != nil {
			return err
		}
		if err := pgtest.Run(t.Context(), database.Superuser,
			fmt.Sprintf("INSERT INTO rlstest.items VALUES ('%s', '%s', 'concurrent')", site1, account1)); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), "SELECT count(*) FROM rlstest.items").Scan(&second)
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != 4 || second != 4 {
		t.Errorf("the reads saw %d and %d rows, want the 4 from before the concurrent commit", first, second)
	}
	if n := count(t, db, platform); n != 5 {
		t.Errorf("the next transaction saw %d rows, want 5 with the concurrent commit", n)
	}
	err = db.ReadTx(t.Context(), platform, func(tx pg.Tx) error {
		_, err := tx.Exec(t.Context(), "DELETE FROM rlstest.items")
		return err
	})
	if err == nil {
		t.Error("a ReadTx deleted rows")
	}
}

// dbtx is the interface sqlc generates for pgx; generated queries take a Tx through it.
type dbtx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

var _ dbtx = pg.Tx{}

// TestTxHidesTheConnection: nothing a Tx hands out or calls back reports the connection: not the rows of Query, not
// a RowScanner that Rows.Scan or Row.Scan gives the row, not a QueryRewriter that Exec, Query or QueryRow run.
// Holding the connection, a caller could run queries the scope does not bind, or outlive the transaction on it. The
// rows and the row offer pgx's methods and nothing more.
func TestTxHidesTheConnection(t *testing.T) {
	t.Parallel()
	database := newDatabase(t)
	seed(t, database)
	db := open(t, database.App)
	ctx := t.Context()
	const query = "SELECT note FROM rlstest.items"
	uses := map[string]func(pg.Tx, *connSpy) error{
		"a RowScanner of Rows.Scan": func(tx pg.Tx, spy *connSpy) error {
			rows, err := tx.Query(ctx, query)
			if err != nil {
				return err
			}
			defer rows.Close()
			if rows.Conn() != nil {
				t.Error("Query's rows handed out their connection")
			}
			sameMethods[pgx.Rows](t, rows)
			if !rows.Next() {
				return fmt.Errorf("no rows: %w", rows.Err())
			}
			return rows.Scan(spy)
		},
		"a RowScanner of Row.Scan": func(tx pg.Tx, spy *connSpy) error {
			row := tx.QueryRow(ctx, query)
			sameMethods[pgx.Row](t, row)
			return row.Scan(spy)
		},
		"a QueryRewriter of Exec": func(tx pg.Tx, spy *connSpy) error {
			_, err := tx.Exec(ctx, query, pgx.QueryExecModeSimpleProtocol, spy)
			return err
		},
		"a QueryRewriter of Query": func(tx pg.Tx, spy *connSpy) error {
			rows, err := tx.Query(ctx, query, spy)
			if err != nil {
				return err
			}
			rows.Close()
			return rows.Err()
		},
		"a QueryRewriter of QueryRow": func(tx pg.Tx, spy *connSpy) error {
			var note string
			return tx.QueryRow(ctx, query, spy).Scan(&note)
		},
	}
	for name, use := range uses {
		spy := new(connSpy)
		if err := db.Tx(ctx, pg.NewScope(true, nil, nil), func(tx pg.Tx) error { return use(tx, spy) }); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !spy.called || spy.conn != nil {
			t.Errorf("%s: called %t, handed the connection %v", name, spy.called, spy.conn)
		}
	}
}

// connSpy records the connection pgx hands it, as a RowScanner or a QueryRewriter.
type connSpy struct {
	called bool
	conn   *pgx.Conn
}

func (s *connSpy) ScanRow(rows pgx.Rows) error {
	s.called, s.conn = true, rows.Conn()
	var note string
	return rows.Scan(&note)
}

func (s *connSpy) RewriteQuery(_ context.Context, conn *pgx.Conn, sql string, args []any) (string, []any, error) {
	s.called, s.conn = true, conn
	return sql, args, nil
}

// TestFailedQueryRows: as pgx promises, a failed Query still returns rows, closed and carrying the error, so a caller
// may defer Close before checking it; they report no connection either.
func TestFailedQueryRows(t *testing.T) {
	t.Parallel()
	db := open(t, newDatabase(t).App)
	err := db.Tx(t.Context(), pg.Scope{}, func(tx pg.Tx) error {
		rows, err := tx.Query(t.Context(), "SELECT FROM no_such_table")
		if rows == nil {
			t.Fatal("a failed Query returned no rows")
		}
		defer rows.Close()
		if rows.Err() == nil || rows.Conn() != nil {
			t.Errorf("rows of a failed Query: error %v, connection %v", rows.Err(), rows.Conn())
		}
		return err
	})
	if err == nil {
		t.Error("a query of a missing table succeeded")
	}
}

// sameMethods reports whether got has exactly the exported methods of the interface I, so pg forwards nothing
// unreviewed.
func sameMethods[I any](t *testing.T, got any) {
	t.Helper()
	methods := func(typ reflect.Type) []string {
		names := make([]string, typ.NumMethod())
		for i := range names {
			names[i] = typ.Method(i).Name
		}
		return names
	}
	if diff := cmp.Diff(methods(reflect.TypeFor[I]()), methods(reflect.TypeOf(got))); diff != "" {
		t.Errorf("%T's methods differ from %v's (-want +got):\n%s", got, reflect.TypeFor[I](), diff)
	}
}

// TestRowLevelSecurityBindsEveryone: the table's owner is filtered too (FORCE), Check accepts the application's
// login, and it refuses a database that misses migrations (logins that escape: check_test.go).
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
	if err := pgtest.Run(t.Context(), database.Superuser, "DELETE FROM platform.goose_platform WHERE version_id > 0"); err != nil {
		t.Fatal(err)
	}
	if err := open(t, database.App).Check(t.Context()); err == nil || !strings.Contains(err.Error(), "axiom migrate") {
		t.Errorf("Check accepted a database without its migrations: %v", err)
	}
}
