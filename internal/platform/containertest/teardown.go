package containertest

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

// reapLabel is the label testcontainers gives a container when its reaper is on; the reaper removes the containers of
// its session that carry it once the test process is gone. The library keeps the constant internal.
const reapLabel = "org.testcontainers.reap"

// removeBound is how long removing a container at the end of a test may take: Docker's 10-second stop grace and room
// for a loaded daemon. A removal that runs out is neither retried nor given longer; a sweep finishes it (sweeper).
const removeBound = 30 * time.Second

// Cleanup removes c when the test ends. On a loaded CI host Docker can take longer than removeBound to stop a container
// whose test has already passed (#605). Such a failure is logged, not failed, when a sweep outside the test will remove
// the container: the CI job's cleanup finds the label WithHostPolicy gives it, the testcontainers reaper its reap
// label. A container with neither would outlive the run, so its failed removal fails the test. The labels are read now,
// from a daemon that has just started the container, not asked of one that has just failed to stop it.
func Cleanup(t testing.TB, c testcontainers.Container, what string) {
	t.Helper()
	info, err := c.Inspect(t.Context())
	if err != nil {
		t.Fatalf("inspect the %s container: %v", what, err)
	}
	sweep := sweeper(info.Config.Labels)
	t.Cleanup(func() {
		// The test's own context is already cancelled when cleanups run.
		ctx, cancel := context.WithTimeout(context.Background(), removeBound)
		defer cancel()
		err := testcontainers.TerminateContainer(c, testcontainers.StopContext(ctx))
		switch {
		case err == nil:
		case sweep != "":
			t.Logf("remove the %s container: %v; left to %s", what, err, sweep)
		default:
			t.Errorf("remove the %s container: %v; no cleanup outside the test finds it, so it leaks", what, err)
		}
	})
}

// sweeper names what removes a container with these labels after the test if the test cannot, or "" when nothing does.
func sweeper(labels map[string]string) string {
	if job := labels[jobLabel]; job != "" {
		return "the CI job's cleanup of " + jobLabel + "=" + job
	}
	if labels[reapLabel] == "true" {
		return "the testcontainers reaper"
	}
	return ""
}
