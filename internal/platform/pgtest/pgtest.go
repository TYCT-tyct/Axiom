// Package pgtest gives tests a PostgreSQL database prepared the way a deployment prepares one: a login role for the
// application, a migration administrator that is not a superuser, and the migrations of platform and the modules a
// test names applied by it. One container serves a whole test binary; each set of modules is migrated once into a
// template, and each test gets its own copy of one, so tests run in parallel and never see each other's rows.
package pgtest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"slices"
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

	// templates holds each set of modules' template, or why it could not be built, by template name. One lock covers
	// building them: migrations create roles, which belong to the whole cluster, so two at once would race.
	templates   = map[string]error{}
	templatesMu sync.Mutex
)

// New returns a database of its own for one test, with platform's migrations and those of modules applied.
func New(t testing.TB, modules ...pg.Module) Database {
	t.Helper()
	once.Do(func() { startErr = start(context.Background()) })
	if startErr != nil {
		t.Fatalf("test database cluster: %v", startErr)
	}
	template, err := migrated(modules)
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

// migrated returns the name of the template migrated with modules, building it the first time. The name is the
// sorted module names, so one set gets one template whatever order it is given in; Migrate orders them itself.
func migrated(modules []pg.Module) (string, error) {
	names := []string{"axiom_template"}
	for _, m := range modules {
		names = append(names, m.Name)
	}
	slices.Sort(names[1:])
	template := strings.Join(names, "_")
	templatesMu.Lock()
	defer templatesMu.Unlock()
	err, built := templates[template]
	if !built {
		err = migrate(template, modules)
		templates[template] = err
	}
	return template, err
}

// migrate creates the template and migrates it twice: the second run must find nothing to do.
func migrate(template string, modules []pg.Module) error {
	ctx := context.Background()
	if err := Run(ctx, at("postgres", "postgres", "postgres"), "CREATE DATABASE "+template,
		"GRANT CREATE ON DATABASE "+template+" TO migrator"); err != nil {
		return err
	}
	for range 2 {
		if err := pg.Migrate(ctx, at("migrator", "migrator", template), modules...); err != nil {
			return err
		}
	}
	return nil
}

// start runs the container and creates the roles a deployment creates. Durability is off for speed; tests of
// durability, migrations under load and performance run elsewhere with it on.
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
	return Run(ctx, at("postgres", "postgres", "postgres"),
		`CREATE ROLE axiom_app LOGIN PASSWORD 'app'`, `CREATE ROLE migrator LOGIN CREATEROLE PASSWORD 'migrator'`)
}

// at is the URL of database in the cluster as user.
func at(user, password, database string) string {
	u := *cluster
	u.User, u.Path, u.RawQuery = url.UserPassword(user, password), "/"+database, "sslmode=disable"
	return u.String()
}
