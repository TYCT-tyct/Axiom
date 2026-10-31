// Package postgres_test exercises the database image and the runbook around it: continuous WAL archiving when the
// backup bucket is configured, and a point-in-time restore that a test actually executes
// (docs/ops/backup-restore.md「WAL 连续归档与时间点恢复」).
package postgres_test

import (
	"context"
	"fmt"
	"io"
	"maps"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"

	"axiom/internal/platform/containertest"
)

const (
	// The bucket and prefix the test writes; the prefix makes the repository path in the bucket look like a
	// deployment's, under a name the logical sets' retention never lists.
	bucket = "axiom-pitr"
	prefix = "pitr-test"

	sourcePassword = "pitr-test-password" //nolint:gosec // a throwaway password for a test container, not a secret
	stanza         = "axiom"
)

// TestPointInTimeRestore runs the whole path against real containers: a base backup and the WAL of a live database go
// into SeaweedFS, a second container restores them to a timestamp, and the rows before that timestamp come back while
// the rows after it do not. The last part starts the image without a bucket and checks that archiving stays off.
func TestPointInTimeRestore(t *testing.T) {
	ctx := t.Context()
	s3 := containertest.StartSeaweedFS(t, bucket)

	source := startPostgres(t, ctx, "", false, backupEnv(s3), map[string]string{
		"POSTGRES_DB":       "axiom",
		"POSTGRES_PASSWORD": sourcePassword,
	})
	assertArchiveMode(t, ctx, source, "on")

	prepareSource(t, ctx, source)
	target := seedAndArchive(t, ctx, source)
	assertArchived(t, ctx, source)
	verifyRestore(t, ctx, s3, source.Image, target)
	assertArchiveOff(t, ctx, source.Image)
}

// prepareSource creates the stanza and the table and takes the base backup, in the order the runbook does. The stanza
// must exist before the archive commands can push anything; a deployment does this once, right after the first start.
func prepareSource(t *testing.T, ctx context.Context, source *testcontainers.DockerContainer) {
	t.Helper()
	exec(t, ctx, source, "pgbackrest", "--stanza="+stanza, "--log-level-console=info", "stanza-create")
	exec(t, ctx, source, "pgbackrest", "--stanza="+stanza, "check")
	sql(t, ctx, source, "CREATE TABLE pitr_events (id bigint PRIMARY KEY, note text)")
	exec(t, ctx, source, "/usr/local/bin/basebackup.sh")
	// A recovery target has to be after the backup's stop time. The backup stop is reported to the second, and a target
	// inside that second is rejected as not later than the backup; waiting one second puts the target in a later second,
	// which is also the only useful case in a runbook.
	time.Sleep(time.Second)
}

// seedAndArchive writes the two committed transactions with the target time between them, then forces the segment that
// holds the second commit into the bucket. The target is read after the first commit, in a statement of its own.
func seedAndArchive(t *testing.T, ctx context.Context, source *testcontainers.DockerContainer) string {
	t.Helper()
	sql(t, ctx, source, "INSERT INTO pitr_events SELECT g, 'before' FROM generate_series(1, 100) g")
	target := sql(t, ctx, source, "SELECT clock_timestamp()")
	sql(t, ctx, source, "INSERT INTO pitr_events SELECT g, 'after' FROM generate_series(101, 150) g")
	wanted := sql(t, ctx, source, "SELECT pg_walfile_name(pg_current_wal_lsn())")
	exec(t, ctx, source, "psql", "-U", "postgres", "-d", "axiom", "-Atc", "SELECT pg_switch_wal()")
	waitArchived(t, ctx, source, wanted)
	return target
}

// assertArchived checks the repository through pgBackRest itself: it is readable from the bucket and holds both parts
// of a PITR.
func assertArchived(t *testing.T, ctx context.Context, source *testcontainers.DockerContainer) {
	t.Helper()
	info := exec(t, ctx, source, "pgbackrest", "--stanza="+stanza, "info")
	for _, want := range []string{"wal archive min/max", "full backup", "cipher: aes-256-cbc"} {
		if !strings.Contains(info, want) {
			t.Fatalf("pgbackrest info has no %q:\n%s", want, info)
		}
	}
}

// verifyRestore starts a second container from the built image and restores into its own empty data directory. It never
// touches the source's volume or port; the restored server promotes and stays up for the row check.
func verifyRestore(t *testing.T, ctx context.Context, s3 containertest.SeaweedFS, image, target string) {
	t.Helper()
	restoreEnv := backupEnv(s3)
	restoreEnv["AXIOM_PITR_TARGET"] = target
	restore := startPostgres(t, ctx, image, true, restoreEnv, nil)
	waitPromoted(t, ctx, restore)
	count, err := strconv.Atoi(sql(t, ctx, restore, "SELECT count(*) FROM pitr_events"))
	if err != nil {
		t.Fatalf("read the restored row count: %v", err)
	}
	after, err := strconv.Atoi(sql(t, ctx, restore, "SELECT count(*) FROM pitr_events WHERE note = 'after'"))
	if err != nil {
		t.Fatalf("read the restored rows after the target: %v", err)
	}
	if count != 100 || after != 0 {
		t.Fatalf("restored rows = %d with %d after the target, want 100 and 0", count, after)
	}
}

// assertArchiveOff starts the image without a bucket; a deployment that does not back up must keep archiving off.
func assertArchiveOff(t *testing.T, ctx context.Context, image string) {
	t.Helper()
	off := startPostgres(t, ctx, image, false, nil, map[string]string{
		"POSTGRES_DB":       "axiom",
		"POSTGRES_PASSWORD": sourcePassword,
	})
	assertArchiveMode(t, ctx, off, "off")
}

func assertArchiveMode(t *testing.T, ctx context.Context, ctr *testcontainers.DockerContainer, want string) {
	t.Helper()
	if got := sql(t, ctx, ctr, "SHOW archive_mode"); got != want {
		t.Fatalf("archive_mode = %q, want %q", got, want)
	}
}

// backupEnv is what deploy/compose.yaml hands the database for pgBackRest. The container reaches SeaweedFS on the
// bridge network, which is the address a deployment's bucket lives at too.
func backupEnv(s3 containertest.SeaweedFS) map[string]string {
	return map[string]string{
		"PGBACKREST_REPO1_PATH":          "/" + prefix + "-pgbackrest",
		"PGBACKREST_REPO1_S3_BUCKET":     bucket,
		"PGBACKREST_REPO1_S3_KEY":        containertest.SeaweedFSAccessKey,
		"PGBACKREST_REPO1_S3_KEY_SECRET": containertest.SeaweedFSSecretKey,
		"PGBACKREST_REPO1_S3_REGION":     "us-east-1",
		"PGBACKREST_REPO1_S3_ENDPOINT":   s3.InternalEndpoint,
		"PGBACKREST_REPO1_S3_URI_STYLE":  "path",
		"PGBACKREST_REPO1_CIPHER_PASS":   "pitr-test-cipher-pass",
	}
}

// startPostgres starts the database image, building it when image is empty, and removes the container when the test
// ends. The first container is the one built from this directory, so its image outlives the containers started from it:
// its cleanup is registered first and therefore runs last.
func startPostgres(t *testing.T, ctx context.Context, image string, restore bool, backup, server map[string]string) *testcontainers.DockerContainer {
	t.Helper()
	env := make(map[string]string, len(backup)+len(server))
	maps.Copy(env, backup)
	maps.Copy(env, server)
	opts := []testcontainers.ContainerCustomizer{
		testcontainers.WithConfigModifier(func(config *container.Config) { config.User = "postgres" }),
		testcontainers.WithExposedPorts("5432/tcp"),
		testcontainers.WithEnv(env),
		testcontainers.WithWaitStrategyAndDeadline(3*time.Minute, wait.ForListeningPort("5432/tcp")),
	}
	if image == "" {
		opts = append(opts, testcontainers.WithDockerfile(testcontainers.FromDockerfile{
			Context: ".", Dockerfile: "Dockerfile",
		}))
	}
	if restore {
		// The pitr service overrides the image's entrypoint the same way (deploy/compose.yaml): the wrapper restores
		// into the empty data directory before the base image's own entrypoint starts the server.
		opts = append(opts, testcontainers.WithEntrypoint("/usr/local/bin/pitr.sh"))
	}
	// Last: it sets a host-config modifier, and it must see the options above.
	opts = append(opts, containertest.WithHostPolicy())
	ctr, err := testcontainers.Run(ctx, image, opts...)
	if err != nil {
		t.Fatalf("start the database image: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := testcontainers.TerminateContainer(ctr, testcontainers.StopContext(stopCtx)); err != nil {
			t.Errorf("remove the database container: %v", err)
		}
	})
	return ctr
}

// waitPromoted waits out recovery: the server accepts read-only connections before it reaches the target, so the port
// being open is not enough.
func waitPromoted(t *testing.T, ctx context.Context, restore *testcontainers.DockerContainer) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		got, err := trySQL(ctx, restore, "SELECT pg_is_in_recovery()")
		if err == nil && got == "f" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the restored server did not promote: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// waitArchived waits until the segment named want, or the one before it, is in the archive. The switch archives the
// segment that held the last commit; the boundary case leaves the commit in the previous segment, which a full segment
// has already archived.
func waitArchived(t *testing.T, ctx context.Context, source *testcontainers.DockerContainer, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		last := sql(t, ctx, source, "SELECT last_archived_wal FROM pg_stat_archiver")
		if last != "" && segmentReached(last, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("segment %s never reached the archive (last %q)", want, last)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// segmentReached reports whether archived has reached want or is one segment behind it. WAL names are a timeline plus a
// fixed-width log and segment number, so the numbers compare numerically.
func segmentReached(archived, want string) bool {
	if len(archived) < 8 || len(want) < 8 {
		return false
	}
	a, err := strconv.ParseUint(archived[8:], 16, 64)
	if err != nil {
		return false
	}
	w, err := strconv.ParseUint(want[8:], 16, 64)
	if err != nil {
		return false
	}
	return a+1 >= w
}

// sql runs one statement in the container's server and returns its single-column output. The container's local socket
// is trusted, so the test needs no password and no connection pool: it drives the image the way the runbook does.
func sql(t *testing.T, ctx context.Context, ctr *testcontainers.DockerContainer, statement string) string {
	t.Helper()
	out, err := trySQL(ctx, ctr, statement)
	if err != nil {
		t.Fatalf("run %q: %v", statement, err)
	}
	return out
}

// trySQL is sql without failing the test, for a server that is still starting.
func trySQL(ctx context.Context, ctr *testcontainers.DockerContainer, statement string) (string, error) {
	out, err := command(ctx, ctr, "psql", "-U", "postgres", "-d", "axiom", "-Atc", statement)
	return strings.TrimSpace(out), err
}

// exec runs a command in the container and fails the test with its output when it exits non-zero.
func exec(t *testing.T, ctx context.Context, ctr *testcontainers.DockerContainer, argv ...string) string {
	t.Helper()
	out, err := command(ctx, ctr, argv...)
	if err != nil {
		t.Fatalf("run %v: %v", argv, err)
	}
	return out
}

func command(ctx context.Context, ctr *testcontainers.DockerContainer, argv ...string) (string, error) {
	code, out, err := ctr.Exec(ctx, argv, tcexec.Multiplexed())
	if err != nil {
		return "", err
	}
	output, readErr := io.ReadAll(out)
	if readErr != nil {
		return "", readErr
	}
	if code != 0 {
		return string(output), fmt.Errorf("exit %d:\n%s", code, output)
	}
	return string(output), nil
}
