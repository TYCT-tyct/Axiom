package containertest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
)

func TestCgroupParent(t *testing.T) {
	t.Setenv("AXIOM_TEST_JOB", "")
	for _, c := range []struct{ name, parent, want string }{
		{"empty", "", "developer.slice"},
		{"ci", "axiom-ci.slice", "axiom-ci.slice"},
		{"other host", "other-ci.slice", "other-ci.slice"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("AXIOM_TEST_CGROUP_PARENT", c.parent)
			checkHostConfig(t, c.want)
		})
	}
}

func TestCgroupParentUnset(t *testing.T) {
	t.Setenv("AXIOM_TEST_CGROUP_PARENT", "")
	if err := os.Unsetenv("AXIOM_TEST_CGROUP_PARENT"); err != nil {
		t.Fatal(err)
	}
	checkHostConfig(t, "developer.slice")
}

func checkHostConfig(t *testing.T, want string) {
	t.Helper()
	var req testcontainers.GenericContainerRequest
	if err := testcontainers.WithHostConfigModifier(func(host *container.HostConfig) {
		host.CgroupParent = "developer.slice"
		host.ShmSize = 1234
	})(&req); err != nil {
		t.Fatal(err)
	}
	if err := WithHostPolicy()(&req); err != nil {
		t.Fatal(err)
	}
	var host container.HostConfig
	req.HostConfigModifier(&host)
	if host.CgroupParent != want || host.ShmSize != 1234 {
		t.Fatalf("host config = %+v, want parent %q and unchanged shared memory", host, want)
	}
}

func TestJobLabel(t *testing.T) {
	for _, job := range []string{"", "123-2-go-test"} {
		t.Run(job, func(t *testing.T) {
			t.Setenv("AXIOM_TEST_JOB", job)
			req := testcontainers.GenericContainerRequest{
				Labels: map[string]string{"service": "kept"},
			}
			if err := WithHostPolicy()(&req); err != nil {
				t.Fatal(err)
			}
			value, exists := req.Labels["axiom.ci.job"]
			if value != job || exists != (job != "") || req.Labels["service"] != "kept" {
				t.Fatalf("labels = %v, want job %q and unchanged service label", req.Labels, job)
			}
		})
	}
}

// The test log is the go command's actual list of cache inputs, so this also catches a future indirect Getenv read.
func TestHostPolicyCacheInputs(t *testing.T) {
	if os.Getenv("AXIOM_HOST_POLICY_HELPER") == "true" {
		var req testcontainers.GenericContainerRequest
		if err := WithHostPolicy()(&req); err != nil {
			t.Fatal(err)
		}
		if req.Labels["axiom.ci.job"] != "cache-check" {
			t.Fatalf("job label = %q", req.Labels["axiom.ci.job"])
		}
		return
	}
	log := filepath.Join(t.TempDir(), "testlog")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHostPolicyCacheInputs$", "-test.testlogfile="+log) //nolint:gosec // re-execute this test binary with a test-owned log path
	cmd.Env = append(os.Environ(), "AXIOM_HOST_POLICY_HELPER=true", "AXIOM_TEST_JOB=cache-check")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v\n%s", err, output)
	}
	data, err := os.ReadFile(log) //nolint:gosec // the child writes this log in the test-owned temporary directory
	if err != nil {
		t.Fatal(err)
	}
	inputs := string(data)
	if strings.Contains(inputs, "getenv AXIOM_TEST_JOB\n") {
		t.Fatalf("job identity entered the test cache key:\n%s", inputs)
	}
	if !strings.Contains(inputs, "getenv AXIOM_TEST_CGROUP_PARENT\n") {
		t.Fatalf("missing control cache input:\n%s", inputs)
	}
}
