package containertest

import (
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
)

func TestCgroupParent(t *testing.T) {
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
	if err := WithHostPolicy(t.Context())(&req); err != nil {
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
			if err := WithHostPolicy(t.Context())(&req); err != nil {
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
		if err := WithHostPolicy(t.Context())(&req); err != nil {
			t.Fatal(err)
		}
		if req.Labels["axiom.ci.job"] != "cache-check" {
			t.Fatalf("job label = %q", req.Labels["axiom.ci.job"])
		}
		if got := Untracked("AXIOM_UNTRACKED_CACHE_INPUT"); got != "untracked-check" {
			t.Fatalf("untracked cache input = %q", got)
		}
		return
	}
	log := filepath.Join(t.TempDir(), "testlog")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHostPolicyCacheInputs$", "-test.testlogfile="+log) //nolint:gosec // re-execute this test binary with a test-owned log path
	cmd.Env = append(os.Environ(), "AXIOM_HOST_POLICY_HELPER=true", "AXIOM_TEST_JOB=cache-check",
		"AXIOM_UNTRACKED_CACHE_INPUT=untracked-check")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v\n%s", err, output)
	}
	data, err := os.ReadFile(log) //nolint:gosec // the child writes this log in the test-owned temporary directory
	if err != nil {
		t.Fatal(err)
	}
	inputs := string(data)
	for _, name := range []string{"AXIOM_TEST_JOB", "AXIOM_UNTRACKED_CACHE_INPUT"} {
		if strings.Contains(inputs, "getenv "+name+"\n") {
			t.Fatalf("%s entered the test cache key:\n%s", name, inputs)
		}
	}
	if !strings.Contains(inputs, "getenv AXIOM_TEST_CGROUP_PARENT\n") {
		t.Fatalf("missing control cache input:\n%s", inputs)
	}
}

// Every exposed port, named once or twice, with or without its protocol, is published on loopback below the ephemeral
// range, so Docker never picks one there.
func TestHostPolicyPublishesBelowTheEphemeralRange(t *testing.T) {
	var req testcontainers.GenericContainerRequest
	req.ExposedPorts = []string{"5432/tcp", "80", "5432/tcp"}
	if err := WithHostPolicy(t.Context())(&req); err != nil {
		t.Fatal(err)
	}
	var host container.HostConfig
	req.HostConfigModifier(&host)
	if len(host.PortBindings) != 2 {
		t.Fatalf("bindings = %v, want one for each of the two ports", host.PortBindings)
	}
	for _, port := range []string{"5432/tcp", "80/tcp"} {
		bindings := host.PortBindings[network.MustParsePort(port)]
		if len(bindings) != 1 {
			t.Fatalf("%s bindings = %v, want one", port, bindings)
		}
		number, err := strconv.Atoi(bindings[0].HostPort)
		if bindings[0].HostIP != netip.MustParseAddr("127.0.0.1") || err != nil || number < firstHostPort || number > lastHostPort {
			t.Fatalf("%s binding = %+v, want 127.0.0.1 and a port from %d to %d", port, bindings[0], firstHostPort, lastHostPort)
		}
	}
}

func TestStartTriesAnotherPortOnlyWhenTheBindLost(t *testing.T) {
	lost := errors.New("start container: failed to set up container networking: driver failed programming external " +
		"connectivity on endpoint x: failed to bind host port 0.0.0.0:46182/tcp: address already in use")
	other := errors.New("start container: image not found")
	for _, c := range []struct {
		name  string
		fails []error
		tries int
		err   error
	}{
		{"started", nil, 1, nil},
		{"lost twice", []error{lost, lost}, 3, nil},
		{"other failure", []error{other, lost}, 1, other},
		{"lost every time", []error{lost, lost, lost, lost, lost, lost, lost, lost, lost}, startAttempts, lost},
	} {
		t.Run(c.name, func(t *testing.T) {
			tries := 0
			_, err := Start(t.Context(), func() (*testcontainers.DockerContainer, error) {
				tries++
				if tries <= len(c.fails) {
					return nil, c.fails[tries-1]
				}
				return new(testcontainers.DockerContainer), nil
			})
			if tries != c.tries || !errors.Is(err, c.err) {
				t.Fatalf("tries = %d, err = %v; want %d and %v", tries, err, c.tries, c.err)
			}
		})
	}
}
