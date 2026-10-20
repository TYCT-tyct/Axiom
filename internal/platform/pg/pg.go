// Package pg is the only way to PostgreSQL. Modules get a transaction whose tenant scope is set before their first
// query and ends with it (docs/security.md「租户隔离」); they never see a pool or a connection, so they cannot run a
// query outside a scoped transaction or leave a setting behind on a pooled connection.
package pg

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// DB is the runtime connection pool, logged in as a role that row-level security binds (Check verifies it).
type DB struct {
	pool   *pgxpool.Pool
	tracer trace.Tracer
}

// Option adjusts Open.
type Option func(*DB)

// WithTracerProvider makes every transaction a span, a child of the span in the context it was given. The span
// carries no statement and no error text, which may quote data.
func WithTracerProvider(tp trace.TracerProvider) Option {
	return func(db *DB) { db.tracer = tp.Tracer("axiom/pg") }
}

// The variables that hold the connection strings: errors name them, so the code that reads them and the code that
// reports on them agree on one spelling.
const (
	DatabaseURLVar        = "AXIOM_DATABASE_URL"
	MigrateDatabaseURLVar = "AXIOM_MIGRATE_DATABASE_URL"
)

// PoolSize is how many connections the process keeps open at most (platform README「连接池」). It follows the gateway's
// gate for database work rather than the machine's cores, so pgx's default does not decide it.
const PoolSize = 64

// errNotDatabaseURL is what both parse failures report: pgxpool parses the string again and then checks the pool_*
// parameters, so a string pgx accepted can still fail there, and neither error may quote the string, which holds a
// password.
var errNotDatabaseURL = errors.New(DatabaseURLVar + ": not a PostgreSQL connection string")

// Open connects to url, AXIOM_DATABASE_URL. Errors name the variable but not its value, which holds a password.
func Open(ctx context.Context, url string, opts ...Option) (*DB, error) {
	// pgxpool removes pool_max_conns from the config it parses, so ask pgx, which keeps the parameter, whether the
	// connection string sizes the pool itself: a deployment that says so owns the number.
	conn, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, errNotDatabaseURL
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errNotDatabaseURL
	}
	if _, set := conn.RuntimeParams["pool_max_conns"]; !set {
		config.MaxConns = PoolSize
	}
	// pgx accepts any duration here, then starts a ticker with it in a goroutine, which panics on one that is not
	// positive. It is the only pool parameter that fails this way: pgx refuses the other bad values while parsing,
	// or reads them as "never" or "at every check".
	if config.HealthCheckPeriod <= 0 {
		return nil, errors.New(DatabaseURLVar + ": pool_health_check_period must be positive")
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect to the database: %w", err)
	}
	db := &DB{pool: pool, tracer: noop.NewTracerProvider().Tracer("axiom/pg")}
	for _, opt := range opts {
		opt(db)
	}
	return db, nil
}

// Close waits for borrowed connections to come back and closes the pool.
func (db *DB) Close() { db.pool.Close() }

// MaxConns is the pool's ceiling: how many connections it may open at once, not how many are open now.
func (db *DB) MaxConns() int32 { return db.pool.Stat().MaxConns() }

// ErrNoRows is what a Row's Scan returns when its query found no row, for the modules that must tell "none" from a
// failure; they may not import pgx.
var ErrNoRows = pgx.ErrNoRows

// SQLState is the SQLSTATE code of the PostgreSQL error in err's chain, or "" when there is none. Modules tell a
// conflict or a refused row from other failures by it without importing pgx.
func SQLState(err error) string {
	if e, ok := errors.AsType[*pgconn.PgError](err); ok {
		return e.Code
	}
	return ""
}

// Scope is the set of rows a transaction may see and write: every row, the rows of some sites, the rows of some
// accounts, or any union of these. The zero value is the empty set.
type Scope struct {
	platform        bool
	sites, accounts []uuid.UUID
	// home is the site of an account scope's account, whose public configuration it reads (platform.sees_site); the
	// zero UUID is none. It widens nothing else: the account's rows are still only its own.
	home uuid.UUID
}

// PlatformScope is every row. Only cmd/ builds it, for the modules that maintain platform and public data; a lint rule
// (forbidigo) refuses it elsewhere, because a module that could build it could read every tenant.
func PlatformScope() Scope { return Scope{platform: true} }

// NewScope builds any scope, for tests in any module. Production code gets the platform scope from PlatformScope and
// tenant scopes from authorization; the same lint rule refuses NewScope outside tests.
func NewScope(platform bool, sites, accounts []uuid.UUID) Scope {
	return Scope{platform: platform, sites: sites, accounts: accounts}
}

// SiteScope is the rows of one site. Only internal/accounts builds it, after authorization; the lint rule refuses it
// elsewhere, as it refuses PlatformScope.
func SiteScope(site uuid.UUID) Scope { return Scope{sites: []uuid.UUID{site}} }

// AccountScope is the rows of one account. Only internal/accounts builds it, after authorization or for the account a
// registration creates; the lint rule refuses it elsewhere.
func AccountScope(account uuid.UUID) Scope { return Scope{accounts: []uuid.UUID{account}} }

// AccountScopeIn is AccountScope that also reads the public configuration of the account's site, such as the payment
// methods its top-up page lists (docs/security.md「租户隔离」). The site comes from the authorization that found the
// account, never from a row the account can write; like AccountScope, only internal/accounts builds it.
func AccountScopeIn(site, account uuid.UUID) Scope {
	return Scope{accounts: []uuid.UUID{account}, home: site}
}

// Tx is a transaction whose scope is already set.
type Tx struct{ tx pgx.Tx }

// The signatures of Exec, Query and QueryRow are the ones sqlc's generated code takes, so a Tx can be handed to it.

// Nothing a Tx hands out or calls back reports the connection: holding it, a caller could run queries the scope does
// not bind, or keep using it after the transaction. pgx hands it out in three places: Rows.Conn, the rows given to a
// RowScanner that is the sole destination of Scan, and RewriteQuery of a QueryRewriter among the leading arguments.

// Exec runs a statement and reports what it did, such as how many rows it changed.
func (t Tx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.tx.Exec(ctx, sql, hideFromRewriters(args)...)
}

// Query runs a query that returns any number of rows. pgx returns rows even with an error, which they also carry, so
// callers may defer Close before checking it; Query keeps that promise.
func (t Tx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	r, err := t.tx.Query(ctx, sql, hideFromRewriters(args)...)
	return rows{r}, err
}

// QueryRow runs a query that returns at most one row.
func (t Tx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return row{t.tx.QueryRow(ctx, sql, hideFromRewriters(args)...)}
}

// SetRole runs the rest of the transaction as role, which the login role must be a member of, like SET LOCAL ROLE:
// the transaction's end returns the connection to the login role, where a plain SET ROLE would stay on the pooled
// connection. A background job uses it to act as a role granted only the tables it writes (docs/security.md「租户隔离」).
// The scope still binds.
func (t Tx) SetRole(ctx context.Context, role string) error {
	if _, err := t.tx.Exec(ctx, "SELECT set_config('role', $1, true)", role); err != nil {
		return fmt.Errorf("set the role %s: %w", role, err)
	}
	return nil
}

// hideFromRewriters wraps each QueryRewriter among the options pgx reads from the front of args so that it is given
// no connection; pgx's own rewriters, such as NamedArgs, do not use it. The caller's slice is left alone.
func hideFromRewriters(args []any) []any {
	for i, arg := range args {
		switch arg := arg.(type) {
		case pgx.QueryExecMode, pgx.QueryResultFormats, pgx.QueryResultFormatsByOID:
		case pgx.QueryRewriter:
			args = slices.Clone(args)
			args[i] = rewriter{arg}
		default:
			return args
		}
	}
	return args
}

type rewriter struct{ inner pgx.QueryRewriter }

func (r rewriter) RewriteQuery(ctx context.Context, _ *pgx.Conn, sql string, args []any) (string, []any, error) {
	return r.inner.RewriteQuery(ctx, nil, sql, args)
}

// hideFromScanner replaces a sole RowScanner destination with one that hands it rows without their connection. pgx
// still makes the call, so a failing ScanRow closes the rows with its error as before.
func hideFromScanner(dest []any) []any {
	if len(dest) == 1 {
		if s, ok := dest[0].(pgx.RowScanner); ok {
			return []any{scanner{s}}
		}
	}
	return dest
}

type scanner struct{ inner pgx.RowScanner }

func (s scanner) ScanRow(r pgx.Rows) error { return s.inner.ScanRow(rows{r}) }

// row is pgx's row, whose Scan hands a RowScanner rows without their connection.
type row struct{ inner pgx.Row }

func (r row) Scan(dest ...any) error { return r.inner.Scan(hideFromScanner(dest)...) }

// rows forwards every method of pgx.Rows except Conn. Forwarding one by one instead of embedding means a method pgx
// adds to Rows stops the build here, rather than reaching callers unreviewed.
type rows struct{ inner pgx.Rows }

var _ pgx.Rows = rows{}

func (r rows) Close()                                       { r.inner.Close() }
func (r rows) Err() error                                   { return r.inner.Err() }
func (r rows) CommandTag() pgconn.CommandTag                { return r.inner.CommandTag() }
func (r rows) FieldDescriptions() []pgconn.FieldDescription { return r.inner.FieldDescriptions() }
func (r rows) Next() bool                                   { return r.inner.Next() }
func (r rows) Scan(dest ...any) error                       { return r.inner.Scan(hideFromScanner(dest)...) }
func (r rows) Values() ([]any, error)                       { return r.inner.Values() }
func (r rows) RawValues() [][]byte                          { return r.inner.RawValues() }
func (r rows) TypeMap() *pgtype.Map                         { return r.inner.TypeMap() }

// Conn is nil, which pgx allows for rows that do not come from a connection.
func (rows) Conn() *pgx.Conn { return nil }

// setScope sets the four settings the platform schema's scope functions read, local to the transaction.
const setScope = `SELECT set_config('axiom.scope_platform', $1, true), set_config('axiom.scope_sites', $2, true),
	set_config('axiom.scope_accounts', $3, true), set_config('axiom.scope_home', $4, true)`

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

// startSpan opens the transaction's span, a child of ctx's span. Its attribute is set only when the span records: an
// unsampled span drops attributes, but Go builds their arguments first, and with no exporter nothing is sampled
// (internal/platform/README.md「遥测」). The span carries no statement and no error text, which may quote data.
func (db *DB) startSpan(ctx context.Context) (context.Context, trace.Span) {
	ctx, span := db.tracer.Start(ctx, "db.tx", trace.WithSpanKind(trace.SpanKindClient))
	if span.IsRecording() {
		span.SetAttributes(attribute.String("db.system", "postgresql"))
	}
	return ctx, span
}

func (db *DB) begin(ctx context.Context, options pgx.TxOptions, scope Scope, fn func(Tx) error) (err error) {
	ctx, span := db.startSpan(ctx)
	defer func() {
		if err != nil {
			span.SetStatus(codes.Error, "transaction failed")
		}
		span.End()
	}()
	return pgx.BeginTxFunc(ctx, db.pool, options, func(tx pgx.Tx) error {
		if err := applyScope(ctx, tx, scope); err != nil {
			return fmt.Errorf("set the scope: %w", err)
		}
		return fn(Tx{tx})
	})
}

// applyScope sets the settings of scope in tx; it is the one place that writes them.
func applyScope(ctx context.Context, tx pgx.Tx, scope Scope) error {
	platform, home := "", ""
	if scope.platform {
		platform = "on"
	}
	if scope.home != (uuid.UUID{}) {
		home = scope.home.String()
	}
	_, err := tx.Exec(ctx, setScope, platform, uuidArray(scope.sites), uuidArray(scope.accounts), home)
	return err
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

// Duplicate reports whether err is a unique constraint refusing a row, and names the constraint, so a module can tell
// a duplicate its users can cause, such as an email already registered, from a failure, without importing pgx.
func Duplicate(err error) (constraint string, ok bool) {
	if e, ok := errors.AsType[*pgconn.PgError](err); ok && e.Code == "23505" {
		return e.ConstraintName, true
	}
	return "", false
}

// IsDataError reports whether the database refused the data itself (SQLSTATE class 22, a malformed value, or 23, a
// violated constraint) rather than failing to take it (a lost connection, a restart, a timeout). A writer that
// retries failures retries only the second kind: the first never succeeds, and retrying it would hold up every row
// behind it.
func IsDataError(err error) bool {
	code := SQLState(err)
	return strings.HasPrefix(code, "22") || strings.HasPrefix(code, "23")
}

// Unavailable reports whether err says the database cannot be reached or is not taking connections now: a failed
// connection, a connection exception (SQLSTATE class 08), a server shutting down or starting (57P01 to 57P03), or one
// out of connections (53300). The reason is a fixed text, not err's own: pgx names the host, user and database in a
// connection error, and a secret may sit in any of them (see redactConnect). A caller that tells a database outage
// from a bug does so by this, without importing pgx.
func Unavailable(err error) (reason string, ok bool) {
	if errors.As(err, new(*pgconn.ConnectError)) {
		return "cannot connect to the database", true
	}
	code := SQLState(err)
	switch {
	case strings.HasPrefix(code, "08"):
		return "the connection to the database failed (SQLSTATE " + code + ")", true
	case code == "57P01", code == "57P02", code == "57P03":
		return "the database is shutting down or starting (SQLSTATE " + code + ")", true
	case code == "53300":
		return "the database has no connection left (SQLSTATE " + code + ")", true
	}
	return "", false
}
