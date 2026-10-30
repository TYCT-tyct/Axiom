package pg

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

//go:embed migrations/*.sql
var embedded embed.FS

// Module is one module's migrations. Its name is its schema, which Migrate creates for axiom_owner before any
// migration runs, and names its version table, platform.goose_<name>.
type Module struct {
	Name       string
	Migrations fs.FS // goose's SQL files, numbered with zero padding so that their names sort as their versions
}

// moduleOrder is the order migrations run in: the modules as the dependency table of docs/architecture.md「模块之间的依赖」
// lists them, bottom up, which a test compares. Every module's policies call platform's functions, so platform comes
// first; a fixed order brings every database to the same state the same way. Its names are Axiom's own schemas.
var moduleOrder = []string{
	"platform", "catalog", "accounts", "protocol", "suppliers", "quality",
	"billing", "routing", "observability", "payments", "gateway", "operations",
}

// ordered is platform's own migrations and modules, in moduleOrder. A name that is not a module, or comes twice, is a
// mistake in how cmd/ assembles the modules, and fails the start.
func ordered(modules []Module) ([]Module, error) {
	files, err := fs.Sub(embedded, "migrations")
	if err != nil {
		panic(err) // the directory is embedded, so this cannot happen
	}
	all := append([]Module{{"platform", files}}, modules...)
	for i, m := range all {
		if !slices.Contains(moduleOrder, m.Name) {
			return nil, fmt.Errorf("migrations of %q: not a module in docs/architecture.md", m.Name)
		}
		if slices.ContainsFunc(all[:i], func(earlier Module) bool { return earlier.Name == m.Name }) {
			return nil, fmt.Errorf("migrations of %q: given twice", m.Name)
		}
	}
	slices.SortFunc(all, func(a, b Module) int {
		return slices.Index(moduleOrder, a.Name) - slices.Index(moduleOrder, b.Name)
	})
	return all, nil
}

func versionTable(module string) string { return "platform.goose_" + module }

// bootstrap prepares the roles migrations need: the owner, which owns every schema and object, and the lookup role,
// which owns only the functions that find a tenant before login (internal/accounts/README.md「登录前的查找」). It runs
// as the administrator from AXIOM_MIGRATE_DATABASE_URL, which needs CREATEROLE and CREATE on the database. The login
// role axiom_app is the deployment's to create, with its password, so no password passes through this program. Roles
// that already exist must still be safe: a superuser or BYPASSRLS role would read past FORCE ROW LEVEL SECURITY, and
// one that can log in is a way into what it reads. The owner may act as the lookup role only because a migration
// hands it each function with ALTER FUNCTION, which takes that. A transaction-level advisory lock keeps concurrent
// runs from racing to create the roles.
const bootstrap = `
SELECT pg_advisory_xact_lock(hashtext('axiom bootstrap'));
DO $$
DECLARE
	role text;
BEGIN
	IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'axiom_app') THEN
		RAISE EXCEPTION 'role axiom_app does not exist: the deployment creates it with LOGIN and a password';
	END IF;
	FOREACH role IN ARRAY ARRAY['axiom_owner', 'axiom_lookup'] LOOP
		IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = role) THEN
			EXECUTE format('CREATE ROLE %I NOLOGIN', role);
		ELSIF EXISTS (SELECT FROM pg_roles WHERE rolname = role AND (rolsuper OR rolbypassrls OR rolcanlogin)) THEN
			RAISE EXCEPTION 'role % must not be a superuser, bypass row-level security or log in', role;
		END IF;
	END LOOP;
END
$$;
GRANT axiom_owner TO CURRENT_USER WITH INHERIT FALSE, SET TRUE;
GRANT axiom_lookup TO axiom_owner WITH INHERIT FALSE, SET TRUE;`

// Migrate brings the database at adminURL, AXIOM_MIGRATE_DATABASE_URL, up to this program: platform's migrations,
// then each module's, in moduleOrder. Migrations run as axiom_owner in platform scope on one connection that holds goose's session
// lock, so concurrent runs wait for each other, and a run that dies leaves the lock with its connection; the next run
// continues from the recorded versions. Every lock is waited for at most about 5 seconds: a long transaction or
// another run makes this one fail instead of queueing the application's queries behind it. A failed connection,
// wherever it happens, is reported without the URL's parts. ctx bounds the whole run, long DDL included: at the deadline
// the run ends its own sessions on the server, so a statement that outlives it, or ignores a cancel, ends too, and the
// run returns within stopGrace of it even if the server stops answering. A connection that is not made within DefaultConnectTimeout fails (MigrateWith sets
// another).
func Migrate(ctx context.Context, adminURL string, modules ...Module) error {
	return MigrateWith(ctx, adminURL, DefaultConnectTimeout, modules...)
}

// DefaultConnectTimeout is how long Migrate waits for each connection: an unreachable server is reported in seconds,
// not left to the operating system's minutes.
const DefaultConnectTimeout = 10 * time.Second

// closeTimeout is how long closing the bootstrap connection may take.
const closeTimeout = 5 * time.Second

// stopGrace is how long after ctx ends the run may take to wind down (end the sessions, release goose's lock, close the
// connections) before MigrateWith gives up waiting. goose releases its lock with a context that cannot be cancelled, so a
// server that stops answering would otherwise hold the run forever; the caller then exits, the connections close with
// the process, and the server ends the session and with it the lock.
const stopGrace = 15 * time.Second

// terminateTimeout is how long ending the migration's sessions may take, the connection to do it included.
const terminateTimeout = 5 * time.Second

// MigrateWith is Migrate with the time each connection may take to be made, from the name lookup to the end of the
// handshake, across all the addresses of the host together. A connect_timeout in the URL can only shorten that.
func MigrateWith(ctx context.Context, adminURL string, connectTimeout time.Duration, modules ...Module) error {
	all, err := ordered(modules)
	if err != nil {
		return err
	}
	if connectTimeout <= 0 {
		return errors.New("migrate: the connect timeout must be positive")
	}
	config, err := pgx.ParseConfig(adminURL)
	if err != nil {
		return errors.New(MigrateDatabaseURLVar + ": not a PostgreSQL connection string")
	}
	config.RuntimeParams["lock_timeout"] = "5s" // the bootstrap's advisory lock and every lock a migration takes
	// Every session of this run carries a name of its own, so that the run can end its sessions, and only those, at the
	// deadline.
	name := "axiom-migrate-" + uuid.NewV4().String()
	config.RuntimeParams["application_name"] = name
	return runBounded(ctx, stopGrace,
		func() error { return endSessions(ctx, config, name, connectTimeout) },
		func() error { return migrate(ctx, config, connectTimeout, all) })
}

// runBounded runs f and returns its result. When ctx ends first it runs stop, which must end what f is waiting for, then
// gives f grace to return and gives up on it if it does not, leaving it running: the caller is about to exit. stop's
// error is returned with f's.
func runBounded(ctx context.Context, grace time.Duration, stop, f func() error) error {
	done := make(chan error, 1)
	go func() { done <- f() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	stopErr := stop()
	select {
	case err := <-done:
		return errors.Join(err, stopErr)
	case <-time.After(grace):
		return errors.Join(fmt.Errorf("migrate did not stop within %s of its deadline: %w", grace, context.Cause(ctx)), stopErr)
	}
}

// endSessions ends every session of the run on the server, from a connection of its own with a few seconds to do it, and
// returns when the server has been told. A cancel request from the sessions' own connections is sent by pgx from the
// background, and a statement can be written to ignore a cancel; a terminate cannot be ignored, and it is counted from
// the run's deadline, not from the start of each statement. The sessions end, so their transactions roll back and the
// advisory lock they hold is released.
func endSessions(ctx context.Context, config *pgx.ConnConfig, name string, connectTimeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminateTimeout)
	defer cancel()
	connectCtx, connectCancel := context.WithTimeout(ctx, connectTimeout)
	conn, err := pgx.ConnectConfig(connectCtx, config)
	connectCancel()
	if err != nil {
		return fmt.Errorf("connect to end the migration's sessions: %w", redactConnect(MigrateDatabaseURLVar, err))
	}
	defer conn.Close(ctx) //nolint:errcheck // the sessions are already told to end
	if _, err := conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE application_name = $1 AND pid <> pg_backend_pid()`, name); err != nil {
		return fmt.Errorf("end the migration's sessions: %w", err)
	}
	return nil
}

// timedConnector gives every new database/sql connection one deadline for the whole connect.
type timedConnector struct {
	driver.Connector
	timeout time.Duration
}

// sessionConnector opens the one session a migration run may have and refuses to open another. The session holds the
// migration lock, so a lost connection must fail the run instead of continuing on a new session without the lock.
type sessionConnector struct {
	driver.Connector
	opened atomic.Bool
}

func (c *sessionConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.opened.Swap(true) {
		return nil, errors.New("the migration session was lost; the migration lock is no longer held")
	}
	return c.Connector.Connect(ctx)
}

func (c timedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	conn, err := c.Connector.Connect(ctx)
	return conn, redactConnect(MigrateDatabaseURLVar, err)
}

func migrate(ctx context.Context, config *pgx.ConnConfig, connectTimeout time.Duration, modules []Module) (err error) {
	if err := prepare(ctx, config, connectTimeout, modules); err != nil {
		return err
	}
	db := sql.OpenDB(&sessionConnector{Connector: timedConnector{Connector: stdlib.GetConnector(*config, stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
		// This session serves only migrations and closes with this run. Platform scope lets data migrations see every
		// tenant while retaining forced RLS; modules never set their own scope.
		_, err := conn.Exec(ctx, "SET ROLE axiom_owner; SET axiom.scope_platform = 'on'")
		return err
	})), timeout: connectTimeout}})
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// Ending the run's only session is what releases the migration lock, so a failure to end it is the run's failure.
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("release the migration lock: %w", closeErr))
		}
	}()
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 5)) // retries every second, 5 times
	if err != nil {
		return err
	}
	// One session and one lock carry the whole run: the history check, each module's pending migrations and the record
	// of what they applied. The lock is taken on the only session this run can open, so a second run cannot commit a
	// migration between the check and the execution, and losing the session fails the run instead of continuing
	// without the lock.
	if err := holdLock(ctx, db, locker); err != nil {
		return err
	}
	// Check every module's recorded history before any pending migration runs: a changed merged migration stops the run
	// while the database is still at the versions the previous release left, not after a later migration committed.
	checked, err := preflight(ctx, db, modules)
	if err != nil {
		return err
	}
	for i, m := range modules {
		if err := up(ctx, db, m); err != nil {
			return fmt.Errorf("migrate %s: %w", m.Name, err)
		}
		// Platform's migrations include the one that creates the record table. Until it exists there is nothing to
		// compare, so the modules after platform are checked now, before any of them applies a migration.
		if i == 0 && !checked && len(modules) > 1 {
			if checked, err = preflight(ctx, db, modules[1:]); err != nil {
				return err
			}
		}
	}
	return nil
}

// holdLock takes goose's session lock on the run's only session. The lock lives on the session, so returning the handle
// to the pool keeps it; every later query uses that session, and db.Close ends it after the run.
func holdLock(ctx context.Context, db *sql.DB, locker lock.SessionLocker) error {
	lockConn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("connect to hold the migration lock: %w", redactConnect(MigrateDatabaseURLVar, err))
	}
	if err := locker.SessionLock(ctx, lockConn); err != nil {
		_ = lockConn.Close() //nolint:errcheck // the run is over
		return fmt.Errorf("hold the migration lock: %w", err)
	}
	if err := lockConn.Close(); err != nil {
		return fmt.Errorf("hold the migration lock: %w", err)
	}
	return nil
}

// queueGrants let the application work jobs: read and write the queue's tables and use its sequences and functions.
const queueGrants = `
GRANT USAGE ON SCHEMA ` + queueSchema + ` TO axiom_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ` + queueSchema + ` TO axiom_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA ` + queueSchema + ` TO axiom_app;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA ` + queueSchema + ` TO axiom_app;`

// migrateQueue brings River's tables up to the version of the library, as axiom_owner, in one transaction that holds a
// lock, so concurrent runs wait for each other. River's migrations are its own and are not goose files; the grants
// repeat on every run, so a table a newer River adds is granted too.
func migrateQueue(ctx context.Context, conn *pgx.Conn) error {
	migrator, err := queueMigrator()
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('axiom queue migrate')); SET LOCAL ROLE axiom_owner"); err != nil {
			return err
		}
		// Migrate wants a pool of its own, with the role switched and without the lock this transaction holds. Every migration
		// River has runs inside one transaction, which TestMigrateMakesTheQueueTables proves on each upgrade of River.
		if _, err := migrator.MigrateTx(ctx, tx, rivermigrate.DirectionUp, nil); err != nil { //nolint:staticcheck // see above
			return err
		}
		_, err := tx.Exec(ctx, queueGrants)
		return err
	})
}

// queueMigrator is River's migrator for the queue's schema. It works on a transaction given to it, so it holds no pool.
func queueMigrator() (*rivermigrate.Migrator[pgx.Tx], error) {
	return rivermigrate.New(riverpgxv5.New(nil), &rivermigrate.Config{Schema: queueSchema, Logger: slog.New(slog.DiscardHandler)})
}

// connectAdmin connects as the migration administrator within connectTimeout.
func connectAdmin(ctx context.Context, config *pgx.ConnConfig, connectTimeout time.Duration) (*pgx.Conn, error) {
	connectCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(connectCtx, config)
	if err != nil {
		return nil, fmt.Errorf("connect as the migration administrator: %w", err)
	}
	return conn, nil
}

func closeAdmin(ctx context.Context, conn *pgx.Conn) {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()
	_ = conn.Close(closeCtx) //nolint:errcheck // the transaction already reported the outcome
}

// up applies one module's pending migrations. The caller holds the migration lock for the whole run, so goose does not
// take it again for each module.
func up(ctx context.Context, db *sql.DB, m Module) error {
	files, err := migrationChecksums(m)
	if err != nil {
		return err
	}
	byVersion := make(map[int64]migrationChecksum, len(files))
	for _, file := range files {
		byVersion[file.version] = file
	}
	store, err := database.NewStore(database.DialectPostgres, versionTable(m.Name))
	if err != nil {
		return err
	}
	records, err := tableExists(ctx, db, checksumTable)
	if err != nil {
		return fmt.Errorf("read the record of applied migrations: %w", err)
	}
	provider, err := goose.NewProvider("", db, m.Migrations,
		goose.WithStore(&checksumStore{Store: store, module: m.Name, files: byVersion}),
		goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return err
	}
	// Migrations this run applied while the record table did not exist yet (a fresh database, or one migrated by a
	// release before it) are baselined once. From then on a version is recorded in the same transaction as its row.
	if !records {
		if err := recordChecksums(ctx, db, m); err != nil {
			return err
		}
	}
	// Refuse a merged migration that changed since the database recorded it: the run stops before a node starts
	// against a schema that its recorded history no longer explains.
	if err := verifyChecksums(ctx, db, m); err != nil {
		return err
	}
	// The application reads the version to refuse a database this program is newer than.
	_, err = db.ExecContext(ctx, "GRANT SELECT ON "+versionTable(m.Name)+" TO axiom_app") //nolint:gosec // ordered admits only the names in moduleOrder
	return err
}

// prepare runs bootstrap, creates each module's schema and the queue's for axiom_owner, which may not create schemas
// itself, in one transaction on its own connection, and then migrates the queue on the same connection: one connection
// more would be one more that a loaded machine may fail to make.
func prepare(ctx context.Context, config *pgx.ConnConfig, connectTimeout time.Duration, modules []Module) error {
	conn, err := connectAdmin(ctx, config, connectTimeout)
	if err != nil {
		return fmt.Errorf("connect as the migration administrator: %w", redactConnect(MigrateDatabaseURLVar, err))
	}
	defer closeAdmin(ctx, conn)
	if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, bootstrap); err != nil {
			return err
		}
		for _, name := range append([]string{queueSchema}, moduleNames(modules)...) {
			schema := pgx.Identifier{name}.Sanitize()
			if _, err := tx.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+schema+" AUTHORIZATION axiom_owner"); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("prepare roles and schemas: %w", err)
	}
	if err := migrateQueue(ctx, conn); err != nil {
		return fmt.Errorf("migrate the queue: %w", err)
	}
	return nil
}

func moduleNames(modules []Module) []string {
	names := make([]string, len(modules))
	for i, m := range modules {
		names[i] = m.Name
	}
	return names
}

// latestVersion is the newest of a module's migrations.
func latestVersion(m Module) (int64, error) {
	names, err := fs.Glob(m.Migrations, "*.sql")
	if err != nil {
		return 0, fmt.Errorf("list the migrations of %s: %w", m.Name, err)
	}
	if len(names) == 0 {
		return 0, fmt.Errorf("no migrations of %s", m.Name)
	}
	return goose.NumericComponent(names[len(names)-1]) // Glob sorts, and the names are zero-padded
}
