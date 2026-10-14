// Package pg is the only way to PostgreSQL. Modules get a transaction whose tenant scope is set before their first
// query and ends with it (docs/security.md「租户隔离」); they never see a pool or a connection, so they cannot run a
// query outside a scoped transaction or leave a setting behind on a pooled connection.
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB is the runtime connection pool, logged in as a role that row-level security binds (Check verifies it).
type DB struct{ pool *pgxpool.Pool }

// Open connects to url, AXIOM_DATABASE_URL. Errors name the variable but not its value, which holds a password.
func Open(ctx context.Context, url string) (*DB, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("AXIOM_DATABASE_URL: not a PostgreSQL connection string")
	}
	// pgx accepts any duration here, then starts a ticker with it in a goroutine, which panics on one that is not
	// positive. It is the only pool parameter that fails this way: pgx refuses the other bad values while parsing,
	// or reads them as "never" or "at every check".
	if config.HealthCheckPeriod <= 0 {
		return nil, errors.New("AXIOM_DATABASE_URL: pool_health_check_period must be positive")
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect to the database: %w", err)
	}
	return &DB{pool}, nil
}

// Close waits for borrowed connections to come back and closes the pool.
func (db *DB) Close() { db.pool.Close() }

// Scope is the set of rows a transaction may see and write: every row, the rows of some sites, the rows of some
// accounts, or any union of these. The zero value is the empty set.
type Scope struct {
	platform        bool
	sites, accounts []uuid.UUID
}

// Tx is a transaction whose scope is already set.
type Tx struct{ tx pgx.Tx }

// The signatures of Exec, Query and QueryRow are the ones sqlc's generated code takes, so a Tx can be handed to it.

// Exec runs a statement and reports what it did, such as how many rows it changed.
func (t Tx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.tx.Exec(ctx, sql, args...)
}

// Query runs a query that returns any number of rows. The rows report no connection: holding it, a caller could run
// queries the scope does not bind, or keep using it after the transaction.
func (t Tx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	r, err := t.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return rows{r}, nil
}

// QueryRow runs a query that returns at most one row.
func (t Tx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return t.tx.QueryRow(ctx, sql, args...)
}

// rows forwards every method of pgx.Rows except Conn. Forwarding one by one instead of embedding means a method pgx
// adds to Rows stops the build here, rather than reaching callers unreviewed.
type rows struct{ inner pgx.Rows }

var _ pgx.Rows = rows{}

func (r rows) Close()                                       { r.inner.Close() }
func (r rows) Err() error                                   { return r.inner.Err() }
func (r rows) CommandTag() pgconn.CommandTag                { return r.inner.CommandTag() }
func (r rows) FieldDescriptions() []pgconn.FieldDescription { return r.inner.FieldDescriptions() }
func (r rows) Next() bool                                   { return r.inner.Next() }
func (r rows) Scan(dest ...any) error                       { return r.inner.Scan(dest...) }
func (r rows) Values() ([]any, error)                       { return r.inner.Values() }
func (r rows) RawValues() [][]byte                          { return r.inner.RawValues() }
func (r rows) TypeMap() *pgtype.Map                         { return r.inner.TypeMap() }

// Conn is nil, which pgx allows for rows that do not come from a connection.
func (rows) Conn() *pgx.Conn { return nil }

// setScope sets the three settings platform.in_scope reads, local to the transaction.
const setScope = `SELECT set_config('axiom.scope_platform', $1, true), set_config('axiom.scope_sites', $2, true),
	set_config('axiom.scope_accounts', $3, true)`

// Tx runs fn in a transaction limited to scope and commits if fn returns nil. An error, a panic or a cancelled
// context rolls it back, and pgx drops a connection whose state is unknown instead of returning it to the pool.
func (db *DB) Tx(ctx context.Context, scope Scope, fn func(Tx) error) error {
	return db.begin(ctx, pgx.TxOptions{}, scope, fn)
}

// ReadTx is Tx for reading several queries as one consistent state, such as a snapshot built from many tables: every
// query sees the database as it was at the transaction's first statement (REPEATABLE READ), and nothing can be
// written. Both are set by BEGIN itself, because PostgreSQL refuses to change the isolation level once the scope's
// query has run.
func (db *DB) ReadTx(ctx context.Context, scope Scope, fn func(Tx) error) error {
	return db.begin(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, scope, fn)
}

func (db *DB) begin(ctx context.Context, options pgx.TxOptions, scope Scope, fn func(Tx) error) error {
	return pgx.BeginTxFunc(ctx, db.pool, options, func(tx pgx.Tx) error {
		platform := ""
		if scope.platform {
			platform = "on"
		}
		if _, err := tx.Exec(ctx, setScope, platform, uuidArray(scope.sites), uuidArray(scope.accounts)); err != nil {
			return fmt.Errorf("set the scope: %w", err)
		}
		return fn(Tx{tx})
	})
}

// uuidArray writes ids as a PostgreSQL array literal; no ids is the empty string, which in_scope reads as none.
func uuidArray(ids []uuid.UUID) string {
	if len(ids) == 0 {
		return ""
	}
	texts := make([]string, len(ids))
	for i, id := range ids {
		texts[i] = id.String()
	}
	return "{" + strings.Join(texts, ",") + "}"
}
