// Package pgtest gives tests a PostgreSQL database prepared the way a deployment prepares one: a login role for the
// application, a migration administrator that is not a superuser, and every migration applied by it. One cluster
// serves all test binaries of a run when `just test` has started it (cmd/withcluster, ClusterEnv); a binary run on its
// own starts a container of its own. Each test gets its own copy of a migrated template, so tests run in parallel and
// never see each other's rows. A copy is dropped when its test ends, so a run's cluster holds only the copies of the
// tests in flight instead of one for every test that has run; the cluster, with its templates, is removed at the end.
package pgtest

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"axiom/internal/platform/containertest"
	"axiom/internal/platform/pg"
)

// Database is a fresh copy of the migrated template.
type Database struct {
	App       string // URL of the application's login role, axiom_app
	Retention string // URL of the call-log retention job's login role, axiom_retention
	Migrator  string // URL of the migration administrator, which may create schemas in this database
	Superuser string // URL of the superuser, for seeding and inspecting from outside the application
}

// ClusterEnv names the environment variable that holds the superuser's URL of a cluster StartCluster prepared. With it
// set, New uses that cluster instead of starting a container.
const ClusterEnv = "PGTEST_CLUSTER_URL"

// templateLockKey is the advisory lock that serialises building module templates in a cluster.
const templateLockKey = 1131

// clusterShmSize is the shared memory of the test cluster's container. PostgreSQL puts a parallel query's segments in
// /dev/shm; Docker's 64 MiB default ran out when four test packages ran at once, and every query then failed with
// "could not resize shared memory segment". 512 MiB is a limit, not an allocation.
const clusterShmSize = 512 << 20

var (
	once      sync.Once
	cluster   *url.URL // the superuser's URL to the cluster, database name left out
	startErr  error
	databases atomic.Int64
	// runID tells this test binary's databases from those of the other binaries in a shared cluster.
	runID = strings.ToLower(rand.Text()[:8])

	templatesMu sync.Mutex
	templates   = map[string]bool{} // module templates already built, by name
)

// New returns a database of its own for one test, dropped when the test's cleanups have run. Given modules, it also
// holds their migrations: the database is a copy of a template that has them, built the first time a set of modules is
// asked for.
func New(t testing.TB, modules ...pg.Module) Database {
	t.Helper()
	ready(t)
	template, err := templateFor(t.Context(), modules)
	if err != nil {
		t.Fatalf("test database template: %v", err)
	}
	return create(t, template)
}

// Clone returns a copy of source with its rows, dropped when the test's cleanups have run. It is for a test that
// starts from a fixture seeded once for many tests; New is the cheaper choice when the migrations alone are the
// starting point.
func Clone(t testing.TB, source Database) Database {
	t.Helper()
	ready(t)
	return create(t, databaseName(t, source.Superuser))
}

// ready points this binary at the cluster of the run, starting one of its own if the run has none.
func ready(t testing.TB) {
	t.Helper()
	once.Do(func() { startErr = connect(context.Background()) })
	if startErr != nil {
		t.Fatalf("test database cluster: %v", startErr)
	}
}

// create makes a database from template for the test, and drops it when the test's cleanups have run.
func create(t testing.TB, template string) Database {
	t.Helper()
	name := fmt.Sprintf("test_%s_%d", runID, databases.Add(1))
	if err := Run(t.Context(), at("postgres", "postgres", "postgres"), "CREATE DATABASE "+name+" TEMPLATE "+template,
		"GRANT CREATE ON DATABASE "+name+" TO axiom_migrator"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// t.Context is canceled before the cleanups run, so the drop needs a deadline of its own.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
		defer cancel()
		// FORCE ends connections the test left open; without it the drop would wait for them and keep the copy's files.
		if err := Run(ctx, at("postgres", "postgres", "postgres"), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop test database %s: %v", name, err)
		}
	})
	return Database{
		App:       at("axiom_app", "app", name),
		Retention: at("axiom_retention", "retention", name),
		Migrator:  at("axiom_migrator", "axiom_migrator", name),
		Superuser: at("postgres", "postgres", name),
	}
}

// databaseName is the database databaseURL names.
func databaseName(t testing.TB, databaseURL string) string {
	t.Helper()
	u, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(u.Path, "/")
}

// templateFor is the template a database with modules is copied from. Migrate grants roles cluster-wide and so must
// not run twice at once, in this binary (the mutex) or in another one of the same cluster (the advisory lock). A
// template is built once per set of module names, under a name it is renamed from when complete, so a binary that dies
// half way leaves no template that looks finished; the modules of a run are the same in every test and binary.
func templateFor(ctx context.Context, modules []pg.Module) (string, error) {
	if len(modules) == 0 {
		return "axiom_template", nil
	}
	names := make([]string, len(modules))
	for i, m := range modules {
		names[i] = m.Name
	}
	slices.Sort(names)
	name := "axiom_template_" + strings.Join(names, "_")
	templatesMu.Lock()
	defer templatesMu.Unlock()
	if templates[name] {
		return name, nil
	}
	if err := ensureTemplate(ctx, name, modules); err != nil {
		return "", err
	}
	templates[name] = true
	return name, nil
}

// ensureTemplate builds the template name in the cluster unless another binary has, holding the cluster's advisory lock.
func ensureTemplate(ctx context.Context, name string, modules []pg.Module) error {
	admin := at("postgres", "postgres", "postgres")
	lock, err := pgx.Connect(ctx, admin)
	if err != nil {
		return err
	}
	defer lock.Close(context.WithoutCancel(ctx)) //nolint:errcheck // closing the session releases the lock
	if _, err := lock.Exec(ctx, "SELECT pg_advisory_lock($1)", templateLockKey); err != nil {
		return err
	}
	var built bool
	if err := lock.QueryRow(ctx, "SELECT EXISTS (SELECT FROM pg_database WHERE datname = $1)", name).Scan(&built); err != nil {
		return err
	}
	if built {
		return nil
	}
	return buildTemplate(ctx, admin, name, modules)
}

// buildTemplate copies axiom_template, migrates the copy and renames it to name. The name of the copy is short and
// fixed: the lock allows one build at a time, and a suffix on name could be cut off at PostgreSQL's 63 bytes.
func buildTemplate(ctx context.Context, admin, name string, modules []pg.Module) error {
	const building = "axiom_template_building"
	if err := Run(ctx, admin, "DROP DATABASE IF EXISTS "+building+" WITH (FORCE)",
		"CREATE DATABASE "+building+" TEMPLATE axiom_template", "GRANT CREATE ON DATABASE "+building+" TO axiom_migrator"); err != nil {
		return err
	}
	if err := pg.Migrate(ctx, at("axiom_migrator", "axiom_migrator", building), modules...); err != nil {
		return err
	}
	return Run(ctx, admin, "ALTER DATABASE "+building+" RENAME TO "+name)
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
	if os.Getenv("TESTCONTAINERS_RYUK_DISABLED") == "true" {
		return nil
	}
	if os.Getenv("RYUK_RECONNECTION_TIMEOUT") != "" || os.Getenv("TESTCONTAINERS_RYUK_RECONNECTION_TIMEOUT") != "" {
		return nil
	}
	return os.Setenv("TESTCONTAINERS_RYUK_RECONNECTION_TIMEOUT", "2m")
}

// windowsDockerHost is where Docker Desktop and the Windows engine listen.
const windowsDockerHost = "npipe:////./pipe/docker_engine"

// dockerHostFor returns the DOCKER_HOST to run with. On Windows without one, testcontainers finds Docker by checking
// that the pipe's path exists with os.Stat, and that check fails while every instance of the pipe is busy, which it is
// when many test binaries start together (each go test package is one): it then falls through to its rootless-Docker
// strategy and fails with "rootless Docker is not supported on Windows", though Docker is running. With DOCKER_HOST set
// it skips the check and dials the pipe, which waits for a free instance. The value is the one testcontainers would have
// found.
func dockerHostFor(goos, current string) string {
	if current == "" && goos == "windows" {
		return windowsDockerHost
	}
	return current
}

// connect points this binary at the cluster of the run, or starts one of its own. Ryuk or CI label cleanup removes
// that container after the binary exits.
func connect(ctx context.Context) error {
	connection := containertest.Untracked(ClusterEnv)
	if connection == "" {
		var err error
		if connection, _, err = StartCluster(ctx); err != nil {
			return err
		}
	}
	var err error
	cluster, err = url.Parse(connection)
	return err
}

// StartCluster runs a PostgreSQL container and prepares it: the roles, and a template with every migration applied.
// It returns the superuser's URL, to be passed in ClusterEnv, and the function that removes the container; a process
// that dies without calling it leaves the container to Ryuk or CI label cleanup. Durability is off for speed; tests
// of durability, migrations under load and performance run elsewhere with it on. max_connections is raised because
// every test binary of a run shares the cluster, each with the pools of its parallel tests. WAL is capped because
// every test copies a template and each copy writes WAL: the default gigabyte per cluster serves recovery, and this
// cluster is removed when the run ends.
func StartCluster(ctx context.Context) (string, func(context.Context) error, error) {
	if err := keepReaperAlive(); err != nil {
		return "", nil, err
	}
	if host := dockerHostFor(runtime.GOOS, os.Getenv("DOCKER_HOST")); host != "" {
		if err := os.Setenv("DOCKER_HOST", host); err != nil {
			return "", nil, err
		}
	}
	container, err := containertest.Start(ctx, func() (*postgres.PostgresContainer, error) {
		return postgres.Run(ctx, "postgres:18.6-alpine",
			postgres.WithUsername("postgres"), postgres.WithPassword("postgres"), postgres.BasicWaitStrategies(),
			testcontainers.WithHostConfigModifier(func(host *container.HostConfig) { host.ShmSize = clusterShmSize }),
			testcontainers.WithCmdArgs("-c", "fsync=off", "-c", "synchronous_commit=off", "-c", "full_page_writes=off",
				"-c", "max_connections=1000", "-c", "min_wal_size=32MB", "-c", "max_wal_size=128MB"),
			containertest.WithHostPolicy(ctx))
	})
	if err != nil {
		return "", nil, err
	}
	terminate := func(ctx context.Context) error {
		return testcontainers.TerminateContainer(container, testcontainers.StopContext(ctx))
	}
	connection, err := prepare(ctx, container)
	if err != nil {
		return "", nil, errors.Join(err, terminate(context.WithoutCancel(ctx)))
	}
	return connection, terminate, nil
}

// prepare creates the roles and axiom_template in the container's cluster and returns its URL.
func prepare(ctx context.Context, container *postgres.PostgresContainer) (string, error) {
	host, err := container.Host(ctx)
	if err != nil {
		return "", err
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return "", err
	}
	if host == "localhost" {
		host = "127.0.0.1" // "localhost" makes pgx try ::1 first, which Docker Desktop's forwarder answers slowly under load
	}
	connection := "postgres://postgres:postgres@" + net.JoinHostPort(host, port.Port()) + "/postgres?sslmode=disable"
	if cluster, err = url.Parse(connection); err != nil {
		return "", err
	}
	if err := Run(ctx, at("postgres", "postgres", "postgres"),
		`CREATE ROLE axiom_app LOGIN PASSWORD 'app'`,
		`CREATE ROLE axiom_retention LOGIN PASSWORD 'retention'`,
		`CREATE ROLE axiom_migrator LOGIN CREATEROLE PASSWORD 'axiom_migrator'`,
		`CREATE DATABASE axiom_template`,
		`GRANT CREATE ON DATABASE axiom_template TO axiom_migrator`); err != nil {
		return "", err
	}
	for range 2 { // the second run finds nothing to do
		if err := pg.Migrate(ctx, at("axiom_migrator", "axiom_migrator", "axiom_template")); err != nil {
			return "", err
		}
	}
	return connection, nil
}

// at is the URL of database in the cluster as user.
func at(user, password, database string) string {
	u := *cluster
	u.User, u.Path, u.RawQuery = url.UserPassword(user, password), "/"+database, "sslmode=disable"
	return u.String()
}
