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
	// nodes is the deployment's node count when WithNodes set it; nodesSet distinguishes zero from no option.
	nodes    int
	nodesSet bool
	// operations marks a command's pool, which does not keep the connection string's eagerly opened minimums beyond
	// the slots the budget reserves for such commands.
	operations bool
	// budget is how Open divided the database's connections when it had nodes; nil otherwise.
	budget *Budget
	// config is how a connection of its own outside both pools logs in: the same URL, session settings and role.
	config *pgx.ConnConfig
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

// PoolSize is the ceiling of each node's two pools together when the database has room for it (platform README「连接池」);
// a connection string may set another ceiling within the node's share.
const PoolSize = 64

// CompletionReserve is the part of the pool ordinary work cannot borrow.
const CompletionReserve = 16

// OutsidePoolConns is what a serve node holds outside its two pools: the session that holds its name, River's LISTEN
// listener and the billing holder's heartbeat and reap (platform README「连接池」).
const OutsidePoolConns = 3

// OperationsReserve is the slot headroom the budget keeps for work that is not a node's pools, beyond the server's own
// reserved slots: a migration, an operator command, a backup and one spare.
const OperationsReserve = 4

// WithCompletionReserve sets the completion reserve of the node's pool. Open validates it against the pool size before
// opening either pool.
func WithCompletionReserve(n int) Option {
	return func(db *DB) { db.reserve = n }
}

// WithNodes sizes the pools from the database's max_connections and the deployment's node count, so that the nodes that
// may be online at once together fit the server. Without it the connection string's pool_max_conns, or PoolSize, sizes
// the pools. A zero or negative count is refused before connecting.
func WithNodes(nodes int) Option {
	return func(db *DB) { db.nodes, db.nodesSet = nodes, true }
}

// WithOperationsPool marks a command's pool, which is not a node's: its ceiling stays the deployment's, but it clears
// the connection string's pool_min_conns and pool_min_idle_conns. The slots the budget reserves outside the nodes are
// shared by a migration, a command, a backup and a spare; one command must not pre-open them all. A command takes the
// few connections its serial work needs.
func WithOperationsPool() Option {
	return func(db *DB) { db.operations = true }
}

// Budget is how Open divided the database's connections between the deployment's nodes. Open reports it when it was
// given the node count; serve logs it at startup.
type Budget struct {
	MaxConnections int // the server's max_connections
	Reserved       int // slots no node may pool: the server's reserved slots and OperationsReserve
	Nodes          int // the nodes that may be online at once, as the deployment configured them
	Allowance      int // this node's share of what is left, before OutsidePoolConns
	Share          int // the most this node's two pools may hold: Allowance - OutsidePoolConns
	Pool           int // the combined ceiling of the two pools: min(Share, PoolSize), or pool_max_conns when set
	Completion     int // the part of Pool reserved for completions
}

// errNotDatabaseURL is what both parse failures report: pgxpool parses the string again and then checks the pool_*
// parameters, so a string pgx accepted can still fail there, and neither error may quote the string, which holds a
// password.
var errNotDatabaseURL = errors.New(DatabaseURLVar + ": not a PostgreSQL connection string")

// Open connects to url, AXIOM_DATABASE_URL. Errors name the variable but not its value, which holds a password.
func Open(ctx context.Context, url string, opts ...Option) (*DB, error) {
	config, sized, err := poolConfig(url)
	if err != nil {
		return nil, err
	}
	db := &DB{reserve: CompletionReserve, tracer: noop.NewTracerProvider().Tracer("axiom/pg")}
	for _, opt := range opts {
		opt(db)
	}
	if db.nodesSet && db.operations {
		return nil, errors.New("pg: WithOperationsPool sizes a command's pool, not a node's")
	}
	if db.nodesSet {
		if db.nodes < 1 {
			return nil, fmt.Errorf("a node count of %d cannot share the database's connections", db.nodes)
		}
		budget, err := readBudget(ctx, config, sized, db.nodes, db.reserve)
		if err != nil {
			return nil, err
		}
		db.budget = &budget
		config.MaxConns = int32(budget.Pool) //nolint:gosec // G115: the budget is at most pool_max_conns or PoolSize
	}
	if db.operations {
		config.MinConns = 0
		config.MinIdleConns = 0
	}
	ordinary, completion, err := splitPoolConfig(config, db.reserve)
	if err != nil {
		return nil, err
	}
	// Kept apart from both pool configs so that a connection of its own can be opened from the same settings later.
	db.config = ordinary.ConnConfig.Copy()
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

func poolConfig(url string) (config *pgxpool.Config, sized bool, err error) {
	// pgxpool removes pool_max_conns from the config it parses, so ask pgx, which keeps the parameter, whether the
	// connection string sizes the pool itself: a deployment that says so owns the number.
	conn, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, false, errNotDatabaseURL
	}
	config, err = pgxpool.ParseConfig(url)
	if err != nil {
		return nil, false, errNotDatabaseURL
	}
	if _, set := conn.RuntimeParams["pool_max_conns"]; !set {
		config.MaxConns = PoolSize
	} else {
		sized = true
	}
	return config, sized, nil
}

// readBudget reads the server's connection limits and divides what is left between the deployment's nodes. A pool
// sized by the connection string must fit this node's share instead of replacing it: the deployment says how much of
// the share to use, not how much the node may take from the other nodes.
func readBudget(ctx context.Context, config *pgxpool.Config, sized bool, nodes, completion int) (Budget, error) {
	conn, err := pgx.ConnectConfig(ctx, config.ConnConfig.Copy())
	if err != nil {
		return Budget{}, redactConnect(DatabaseURLVar, err)
	}
	defer func() {
		_ = conn.Close(context.WithoutCancel(ctx)) //nolint:errcheck // the budget's error is the one to report
	}()
	var maxConnections, superuserReserved, reserved int
	err = conn.QueryRow(ctx, `SELECT current_setting('max_connections')::int,
		current_setting('superuser_reserved_connections')::int,
		coalesce(current_setting('reserved_connections', true), '0')::int`).Scan(&maxConnections, &superuserReserved, &reserved)
	if err != nil {
		return Budget{}, fmt.Errorf("%s: read the server's connection limits: %w", DatabaseURLVar, err)
	}
	budget, err := sizePool(maxConnections, superuserReserved+reserved+OperationsReserve, nodes, completion)
	if err != nil {
		return Budget{}, err
	}
	if sized {
		if requested := int(config.MaxConns); requested > budget.Share {
			return Budget{}, fmt.Errorf("%s: pool_max_conns %d exceeds the %d connections one of %d nodes may pool: "+
				"max_connections %d, %d reserved slots, %d connections outside each node's pools",
				DatabaseURLVar, requested, budget.Share, nodes, maxConnections, budget.Reserved, OutsidePoolConns)
		}
		budget.Pool = int(config.MaxConns)
	}
	return budget, nil
}

// sizePool is the arithmetic of the connection budget, separate from the server that supplies its inputs. It refuses a
// deployment in which every node's share, less the connections outside its pools, cannot hold the completion reserve
// and an ordinary connection.
func sizePool(maxConnections, reserved, nodes, completion int) (Budget, error) {
	if nodes <= 0 {
		return Budget{}, fmt.Errorf("a node count of %d cannot share the database's connections", nodes)
	}
	budget := Budget{MaxConnections: maxConnections, Reserved: reserved, Nodes: nodes, Completion: completion}
	budget.Allowance = (maxConnections - reserved) / nodes
	budget.Share = budget.Allowance - OutsidePoolConns
	if budget.Share <= completion {
		return Budget{}, fmt.Errorf("%s: the connection budget cannot fit %d nodes: max_connections %d, %d reserved slots, "+
			"%d connections per node, %d outside the pools and %d for completions",
			DatabaseURLVar, nodes, maxConnections, reserved, budget.Allowance, OutsidePoolConns, completion)
	}
	budget.Pool = min(budget.Share, PoolSize)
	return budget, nil
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

// Budget is how Open divided the database's connections when the deployment gave its node count; ok is false when the
// connection string or PoolSize sized the pools instead.
func (db *DB) Budget() (Budget, bool) {
	if db.budget == nil {
		return Budget{}, false
	}
	return *db.budget, true
}

type completionKey struct{}

// CompletionContext selects the reserved pool for a gateway extension, settlement or orphan settlement.
// It changes neither the transaction's tenant scope nor its deadline.
func CompletionContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, completionKey{}, true)
}
