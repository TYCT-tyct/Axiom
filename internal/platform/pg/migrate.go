package pg

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var embedded embed.FS

// migrations are platform's own; each module brings its own set and version table as it arrives.
func migrations() fs.FS {
	files, err := fs.Sub(embedded, "migrations")
	if err != nil {
		panic(err) // the directory is embedded, so this cannot happen
	}
	return files
}

const versionTable = "platform.goose_platform"

// bootstrap prepares what migrations run as and in: the owner role, which owns every schema and object but cannot
// log in, and the platform schema. It runs as the administrator from AXIOM_MIGRATE_DATABASE_URL, which needs
// CREATEROLE and CREATE on the database. The login role axiom_app is the deployment's to create, with its password,
// so no password passes through this program. An owner role that already exists must still be one: a superuser or
// BYPASSRLS owner would read past FORCE ROW LEVEL SECURITY, and one that can log in is a way into every table. A
// transaction-level advisory lock keeps concurrent runs from racing to create the role.
const bootstrap = `
SELECT pg_advisory_xact_lock(hashtext('axiom bootstrap'));
DO $$
BEGIN
	IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'axiom_app') THEN
		RAISE EXCEPTION 'role axiom_app does not exist: the deployment creates it with LOGIN and a password';
	END IF;
	IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'axiom_owner') THEN
		CREATE ROLE axiom_owner NOLOGIN;
	ELSIF EXISTS (SELECT FROM pg_roles WHERE rolname = 'axiom_owner' AND (rolsuper OR rolbypassrls OR rolcanlogin)) THEN
		RAISE EXCEPTION 'role axiom_owner must not be a superuser, bypass row-level security or log in';
	END IF;
END
$$;
GRANT axiom_owner TO CURRENT_USER WITH INHERIT FALSE, SET TRUE;
CREATE SCHEMA IF NOT EXISTS platform AUTHORIZATION axiom_owner;`

// Migrate brings the database at adminURL, AXIOM_MIGRATE_DATABASE_URL, up to this program. Migrations run as
// axiom_owner on one connection that holds goose's session lock, so concurrent runs wait for each other, and a run
// that dies leaves the lock with its connection; the next run continues from the recorded version. Every lock is
// waited for at most about 5 seconds: a long transaction or another run makes this one fail instead of queueing the
// application's queries behind it.
func Migrate(ctx context.Context, adminURL string) error {
	config, err := pgx.ParseConfig(adminURL)
	if err != nil {
		return errors.New("AXIOM_MIGRATE_DATABASE_URL: not a PostgreSQL connection string")
	}
	config.RuntimeParams["lock_timeout"] = "5s" // the bootstrap's advisory lock and every lock a migration takes
	if err := prepare(ctx, config); err != nil {
		return redactConnect("AXIOM_MIGRATE_DATABASE_URL", err)
	}
	db := stdlib.OpenDB(*config, stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE axiom_owner")
		return err
	}))
	defer db.Close() //nolint:errcheck // closing a pool whose work is done reports nothing worth acting on
	store, err := database.NewStore(database.DialectPostgres, versionTable)
	if err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 5)) // goose retries every second, 5 times
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider("", db, migrations(),
		goose.WithStore(store), goose.WithSessionLocker(locker), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return redactConnect("AXIOM_MIGRATE_DATABASE_URL", fmt.Errorf("migrate platform: %w", err))
	}
	// The application reads the version to refuse a database this program is newer than.
	if _, err := db.ExecContext(ctx, "GRANT SELECT ON "+versionTable+" TO axiom_app"); err != nil {
		return fmt.Errorf("grant the version table: %w", err)
	}
	return nil
}

// prepare runs bootstrap in one transaction on its own connection.
func prepare(ctx context.Context, config *pgx.ConnConfig) error {
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("connect as the migration administrator: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx)) //nolint:errcheck // the transaction already reported the outcome
	if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, bootstrap)
		return err
	}); err != nil {
		return fmt.Errorf("prepare roles and schemas: %w", err)
	}
	return nil
}

// latestVersion is the newest platform migration this program carries.
func latestVersion() (int64, error) {
	names, err := fs.Glob(migrations(), "*.sql")
	if err != nil || len(names) == 0 {
		return 0, fmt.Errorf("no embedded migrations: %w", err)
	}
	return goose.NumericComponent(names[len(names)-1]) // Glob sorts, and the names are zero-padded
}
