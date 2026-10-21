package pg

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// DB is the runtime connection pool, logged in as a role that row-level security binds (Check verifies it).
type DB struct {
	pool       *pgxpool.Pool
	completion *pgxpool.Pool
	reserve    int
	tracer     trace.Tracer
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
	CompletionReserveVar  = "AXIOM_DB_COMPLETION_RESERVE"
	MigrateDatabaseURLVar = "AXIOM_MIGRATE_DATABASE_URL"
)

// PoolSize is the combined ceiling of the ordinary and completion pools (platform README「连接池」).
const PoolSize = 64

// CompletionReserve is the part of PoolSize ordinary work cannot borrow.
const CompletionReserve = 16

// WithCompletionReserve sets the completion share of pool_max_conns. Open validates both before opening either pool.
func WithCompletionReserve(n int) Option {
	return func(db *DB) { db.reserve = n }
}

// errNotDatabaseURL is what both parse failures report: pgxpool parses the string again and then checks the pool_*
// parameters, so a string pgx accepted can still fail there, and neither error may quote the string, which holds a
// password.
var errNotDatabaseURL = errors.New(DatabaseURLVar + ": not a PostgreSQL connection string")

// Open connects to url, AXIOM_DATABASE_URL. Errors name the variable but not its value, which holds a password.
func Open(ctx context.Context, url string, opts ...Option) (*DB, error) {
	config, err := poolConfig(url)
	if err != nil {
		return nil, err
	}
	db := &DB{reserve: CompletionReserve, tracer: noop.NewTracerProvider().Tracer("axiom/pg")}
	for _, opt := range opts {
		opt(db)
	}
	ordinary, completion, err := splitPoolConfig(config, db.reserve)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, ordinary)
	if err != nil {
		return nil, fmt.Errorf("connect to the database: %w", err)
	}
	db.pool = pool
	db.completion, err = pgxpool.NewWithConfig(ctx, completion)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect the completion pool: %w", err)
	}
	return db, nil
}

func poolConfig(url string) (*pgxpool.Config, error) {
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
	return config, nil
}

func splitPoolConfig(config *pgxpool.Config, reserve int) (*pgxpool.Config, *pgxpool.Config, error) {
	if reserve <= 0 || int64(reserve) >= int64(config.MaxConns) {
		return nil, nil, fmt.Errorf("%s: completion reserve (%s) must be positive and below pool_max_conns %d", DatabaseURLVar, CompletionReserveVar, config.MaxConns)
	}
	ordinary := config.Copy()
	kept := int32(reserve) //nolint:gosec // G115: reserve is below MaxConns, which is an int32
	ordinary.MaxConns -= kept
	config.MaxConns = kept
	// Applying the URL's minimum to both pools would exceed the configured budget.
	if ordinary.MinConns > ordinary.MaxConns || ordinary.MinIdleConns > ordinary.MaxConns {
		return nil, nil, fmt.Errorf("%s: pool_min_conns %d and pool_min_idle_conns %d must not exceed ordinary pool capacity %d", DatabaseURLVar, ordinary.MinConns, ordinary.MinIdleConns, ordinary.MaxConns)
	}
	config.MinConns, config.MinIdleConns = 0, 0
	// pgx accepts any duration here, then starts a ticker with it in a goroutine, which panics on one that is not
	// positive. It is the only pool parameter that fails this way: pgx refuses the other bad values while parsing,
	// or reads them as "never" or "at every check".
	if config.HealthCheckPeriod <= 0 {
		return nil, nil, errors.New(DatabaseURLVar + ": pool_health_check_period must be positive")
	}
	return ordinary, config, nil
}

// Close waits for borrowed connections to come back and closes both pools.
func (db *DB) Close() {
	db.pool.Close()
	db.completion.Close()
}

// MaxConns is the combined ceiling of both pools, excluding River's detached LISTEN connection.
func (db *DB) MaxConns() int32 { return db.pool.Stat().MaxConns() + db.completion.Stat().MaxConns() }

// CompletionConns is the part of MaxConns reserved for completions.
func (db *DB) CompletionConns() int { return db.reserve }

type completionKey struct{}

// CompletionContext selects the reserved pool for a gateway extension, settlement or orphan settlement.
// It changes neither the transaction's tenant scope nor its deadline.
func CompletionContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, completionKey{}, true)
}
