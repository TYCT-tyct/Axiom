// Package backup exercises the database image and the runbook around it: continuous WAL archiving when the backup
// bucket is configured, and a point-in-time restore that a test actually executes
// (docs/ops/backup-restore.md「WAL 连续归档与时间点恢复」).
package backup

import (
	"context"
	"fmt"
	"io"
	"maps"
	"path/filepath"
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
	assertWALPrefix(t, ctx, source)

	prepareSource(t, ctx, source)
	target := seedAndArchive(t, ctx, source)
	assertArchived(t, ctx, source)
	verifyRestore(t, ctx, s3, source.Image, target)
	assertArchiveOff(t, ctx, source.Image)
}

// assertWALPrefix checks the wrapper's one rule: a trailing slash is normalized before the default archive prefix is
// derived, and an archive prefix equal to or under the logical backup prefix is refused before the server starts.
func assertWALPrefix(t *testing.T, ctx context.Context, source *testcontainers.DockerContainer) {
	t.Helper()
	path, err := command(ctx, source, "env", "PGBACKREST_REPO1_PATH=", "AXIOM_BACKUP_PREFIX=axiom/", "wal-path.sh")
	if err != nil {
		t.Fatalf("the normalized trailing slash was refused: %v", err)
	}
	if got := strings.TrimSpace(path); got != "/axiom-pgbackrest" {
		t.Fatalf("the archive path for AXIOM_BACKUP_PREFIX=axiom/ is %q, want /axiom-pgbackrest", got)
	}
	if _, err := command(ctx, source, "env", "PGBACKREST_REPO1_PATH=", "AXIOM_BACKUP_PREFIX=axiom/", "pgbackrest", "version"); err != nil {
		t.Fatalf("pgbackrest refused the normalized trailing slash: %v", err)
	}
	out, err := command(ctx, source, "env", "PGBACKREST_REPO1_PATH=", "AXIOM_BACKUP_PREFIX=axiom", "AXIOM_BACKUP_WAL_PREFIX=axiom", "pgbackrest", "version")
	if err == nil {
		t.Fatalf("an archive prefix equal to the logical prefix was accepted:\n%s", out)
	}
	if !strings.Contains(out, "overlaps the logical backup prefix") && !strings.Contains(err.Error(), "overlaps the logical backup prefix") {
		t.Fatalf("the overlap was refused without naming it: output %q, error %v", out, err)
	}
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
	// pg_switch_wal returns an LSN at the segment boundary; l - 1 names the segment it closed on either side of that
	// boundary. That segment, not the one current before the switch, has to reach the bucket: waiting for the previous
	// one (or accepting one behind) would let the restore start before the target's WAL exists.
	archived := sql(t, ctx, source, "SELECT pg_walfile_name(l - 1) FROM (SELECT pg_switch_wal() AS l) s")
	waitArchived(t, ctx, source, archived)
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

// assertArchiveOff starts the image the way scripts/ops/restore.sh starts a restored cluster: the logical set bucket is
// still configured for the restore jobs, but AXIOM_BACKUP_WAL_BUCKET is empty, so the database must not archive.
func assertArchiveOff(t *testing.T, ctx context.Context, image string) {
	t.Helper()
	off := startPostgres(t, ctx, image, false, map[string]string{
		"AXIOM_BACKUP_BUCKET":        "axiom-backup",
		"AXIOM_BACKUP_PREFIX":        prefix,
		"PGBACKREST_REPO1_S3_BUCKET": "",
	}, map[string]string{
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
		"AXIOM_BACKUP_PREFIX":            prefix,
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
// ends. The first container is the one built from deploy/postgres, so its image outlives the containers started from it:
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
			Context: filepath.Join("..", "..", "..", "deploy", "postgres"), Dockerfile: "Dockerfile",
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
	logged := false
	for {
		got, err := trySQL(ctx, restore, "SELECT pg_is_in_recovery()")
		if err == nil && got == "f" {
			return
		}
		if err != nil && !logged && strings.Contains(err.Error(), "is not running") {
			logRestoreFailure(t, ctx, restore)
			logged = true
		}
		if time.Now().After(deadline) {
			t.Fatalf("the restored server did not promote: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// logRestoreFailure prints the wrapper's output once when the restore container exits before promotion; without it the
// only symptom is an exec error on a container that is no longer running.
func logRestoreFailure(t *testing.T, ctx context.Context, restore *testcontainers.DockerContainer) {
	t.Helper()
	if inspect, err := restore.Inspect(ctx); err == nil {
		for _, env := range inspect.Config.Env {
			if strings.Contains(env, "AXIOM_BACKUP_PREFIX") || strings.Contains(env, "AXIOM_BACKUP_WAL_PREFIX") || strings.Contains(env, "PGBACKREST_REPO1_PATH") || strings.Contains(env, "PGBACKREST_REPO1_S3_BUCKET") {
				t.Logf("restore env: %s", env)
			}
		}
	}
	logs, err := restore.Logs(ctx)
	if err != nil {
		return
	}
	defer func() {
		if err := logs.Close(); err != nil {
			t.Logf("close the restored container's logs: %v", err)
		}
	}()
	if body, err := io.ReadAll(logs); err == nil {
		t.Logf("the restored container exited:\n%s", body)
	}
}

// waitArchived waits until the segment named want is in the archive. Accepting the segment before it would pass as soon
// as the switch closed the previous one, before the target's WAL was uploaded.
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

// segmentReached reports whether the archive has reached want. WAL names are a timeline plus a fixed-width log and
// segment number, so the numbers compare numerically.
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
	return a >= w
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
