package containertest

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

// TestCleanupStopFails makes Docker's stop fail once, as the loaded CI host did in #605, and runs the test in a child
// process with the reaper off, so the CI job label is the only sweep: the child passes with the label and fails
// without it.
func TestCleanupStopFails(t *testing.T) {
	if os.Getenv("AXIOM_TEARDOWN_HELPER") == "true" {
		stopFailsOnce(t)
		return
	}
	if testing.Short() {
		t.Skip("starts a container")
	}
	job := Untracked("AXIOM_TEST_JOB")
	if job == "" {
		job = "teardown-check"
	}
	for _, c := range []struct {
		name, job, want string
		pass            bool
	}{
		{"labelled", job, "left to the CI job's cleanup of axiom.ci.job=" + job, true},
		{"unlabelled", "", "no cleanup outside the test finds it, so it leaks", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCleanupStopFails$", "-test.v") //nolint:gosec // re-execute this test binary
			cmd.Env = append(os.Environ(), "AXIOM_TEARDOWN_HELPER=true", "TESTCONTAINERS_RYUK_DISABLED=true", "AXIOM_TEST_JOB="+c.job)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if err != nil && !errors.As(err, &exit) {
				t.Fatalf("run the child: %v", err)
			}
			if (err == nil) != c.pass || !strings.Contains(string(output), c.want) {
				t.Fatalf("child passed = %t, want %t and %q in its output:\n%s", err == nil, c.pass, c.want, output)
			}
		})
	}
}

func stopFailsOnce(t *testing.T) {
	var failed atomic.Bool
	c, err := Start(t.Context(), func() (*testcontainers.DockerContainer, error) {
		return testcontainers.Run(t.Context(), "nginx:alpine", testcontainers.WithExposedPorts("80/tcp"),
			testcontainers.WithAdditionalLifecycleHooks(testcontainers.ContainerLifecycleHooks{
				PreStops: []testcontainers.ContainerHook{func(context.Context, testcontainers.Container) error {
					if failed.CompareAndSwap(false, true) {
						return errors.New("injected: Post /stop?t=10: context deadline exceeded")
					}
					return nil
				}},
			}),
			WithHostPolicy(t.Context()))
	})
	if err != nil {
		t.Fatalf("start the container: %v", err)
	}
	// Registered first, so it runs after Cleanup's failed removal: the container goes now, sweep or not.
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(c, testcontainers.StopTimeout(0)); err != nil {
			t.Errorf("remove the container after the injected failure: %v", err)
		}
	})
	Cleanup(t, c, "injected")
}

func TestSweeper(t *testing.T) {
	for _, c := range []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"none", nil, ""},
		{"job", map[string]string{jobLabel: "1-1-go-test"}, "the CI job's cleanup of axiom.ci.job=1-1-go-test"},
		{"reaper", map[string]string{reapLabel: "true"}, "the testcontainers reaper"},
		{"both", map[string]string{jobLabel: "1-1-go-test", reapLabel: "true"}, "the CI job's cleanup of axiom.ci.job=1-1-go-test"},
		{"empty job, reaper off", map[string]string{jobLabel: "", reapLabel: "false"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := sweeper(c.labels); got != c.want {
				t.Fatalf("sweeper(%v) = %q, want %q", c.labels, got, c.want)
			}
		})
	}
}
