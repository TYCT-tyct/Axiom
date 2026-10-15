// Package pgtest gives tests a PostgreSQL database prepared the way a deployment prepares one: a login role for the
// application, a migration administrator that is not a superuser, and every migration applied by it. One container
// serves a whole test binary; each test gets its own copy of a migrated template, so tests run in parallel and
// never see each other's rows.
package pgtest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"axiom/internal/platform/pg"
)

// Database is a fresh copy of the migrated template.
type Database struct {
	App       string // URL of the application's login role, axiom_app
	Migrator  string // URL of the migration administrator, which may create schemas in this database
	Superuser string // URL of the superuser, for seeding and inspecting from outside the application
}

var (
	once      sync.Once
	cluster   *url.URL // the superuser's URL to the container, database name left out
	startErr  error
	databases atomic.Int64
)

// New returns a database of its own for one test, migrated for platform and for the modules given, which a test of a
// module passes, as cmd/axiom does for the program.
func New(t testing.TB, modules ...pg.Module) Database {
	t.Helper()
	once.Do(func() { startErr = start(context.Background()) })
	if startErr != nil {
		t.Fatalf("test database cluster: %v", startErr)
	}
	template, err := templateFor(t.Context(), modules)
	if err != nil {
		t.Fatalf("test database template: %v", err)
	}
	name := fmt.Sprintf("test_%d", databases.Add(1))
	if err := Run(t.Context(), at("postgres", "postgres", "postgres"), "CREATE DATABASE "+name+" TEMPLATE "+template,
		"GRANT CREATE ON DATABASE "+name+" TO migrator"); err != nil {
		t.Fatal(err)
	}
	return Database{
		App:       at("axiom_app", "app", name),
		Migrator:  at("migrator", "migrator", name),
		Superuser: at("postgres", "postgres", name),
	}
}

var (
	templatesMu sync.Mutex
	templates   = map[string]string{} // the modules' names, in order, to the template migrated for them
)

// templateFor is the template that holds platform's migrations and those of modules, made once per set of modules
// from the platform template: copying beats migrating in every test. Migrate grants roles cluster-wide, so the
// templates are made one at a time.
func templateFor(ctx context.Context, modules []pg.Module) (string, error) {
	if len(modules) == 0 {
		return "axiom_template", nil
	}
	names := make([]string, len(modules))
	for i, m := range modules {
		names[i] = m.Name
	}
	key := strings.Join(names, ",")
	templatesMu.Lock()
	defer templatesMu.Unlock()
	if name, ok := templates[key]; ok {
		return name, nil
	}
	name := fmt.Sprintf("axiom_template_%d", len(templates))
	if err := Run(ctx, at("postgres", "postgres", "postgres"), "CREATE DATABASE "+name+" TEMPLATE axiom_template",
		"GRANT CREATE ON DATABASE "+name+" TO migrator"); err != nil {
		return "", err
	}
	if err := pg.Migrate(ctx, at("migrator", "migrator", name), modules...); err != nil {
		return "", err
	}
	templates[key] = name
	return name, nil
}

// Run executes statements one after another on a new connection to databaseURL.
func Run(ctx context.Context, databaseURL string, statements ...string) error {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer conn.Close(context.WithoutCancel(ctx)) //nolint:errcheck // the statements already reported the outcome
	for _, statement := range statements {
		if _, err := conn.Exec(ctx, statement); err != nil {
			return fmt.Errorf("%s: %w", statement, err)
		}
	}
	return nil
}

// keepReaperAlive works around testcontainers/testcontainers-go#3867 until a release with its fix is in go.mod. All
// test binaries of one `go test ./...` share one Ryuk reaper, which exits 10 seconds after its last client leaves;
// v0.44.0 reuses a reaper that is exiting and waits out its 60-second startup timeout, so a package that starts after
// such an idle gap fails. Keeping the reaper longer than any gap closes the window. It is also how long a finished
// run's containers outlive it, since the reaper removes them, so the value stays short. An explicit setting wins.
func keepReaperAlive() error {
	if os.Getenv("RYUK_RECONNECTION_TIMEOUT") != "" || os.Getenv("TESTCONTAINERS_RYUK_RECONNECTION_TIMEOUT") != "" {
		return nil
	}
	return os.Setenv("TESTCONTAINERS_RYUK_RECONNECTION_TIMEOUT", "2m")
}

// start runs the container and builds the template. Durability is off for speed; tests of durability, migrations
// under load and performance run elsewhere with it on.
func start(ctx context.Context) error {
	if err := keepReaperAlive(); err != nil {
		return err
	}
	container, err := postgres.Run(ctx, "postgres:18.6-alpine",
		postgres.WithUsername("postgres"), postgres.WithPassword("postgres"), postgres.BasicWaitStrategies(),
		testcontainers.WithCmdArgs("-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off"))
	if err != nil {
		return err
	}
	connection, err := container.ConnectionString(ctx)
	if err != nil {
		return err
	}
	if cluster, err = url.Parse(connection); err != nil {
		return err
	}
	if err := Run(ctx, at("postgres", "postgres", "postgres"),
		`CREATE ROLE axiom_app LOGIN PASSWORD 'app'`,
		`CREATE ROLE migrator LOGIN CREATEROLE PASSWORD 'migrator'`,
		`CREATE DATABASE axiom_template`,
		`GRANT CREATE ON DATABASE axiom_template TO migrator`); err != nil {
		return err
	}
	for range 2 { // the second run finds nothing to do
		if err := pg.Migrate(ctx, at("migrator", "migrator", "axiom_template")); err != nil {
			return err
		}
	}
	return nil
}

// at is the URL of database in the cluster as user.
func at(user, password, database string) string {
	u := *cluster
	u.User, u.Path, u.RawQuery = url.UserPassword(user, password), "/"+database, "sslmode=disable"
	return u.String()
}
