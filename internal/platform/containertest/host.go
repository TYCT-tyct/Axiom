// Package containertest holds the host policy shared by test containers.
package containertest

import (
	"os"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
)

// jobLabel carries AXIOM_TEST_JOB; scripts/ci/clean-test-containers.sh removes the containers that have it.
const jobLabel = "axiom.ci.job"

// WithHostPolicy keeps test services inside the CI host's resource budget and labels them for cleanup. Unset or
// empty variables preserve the developer's configuration. Apply it after any container-specific host configuration.
func WithHostPolicy() testcontainers.CustomizeRequestOption {
	parent := os.Getenv("AXIOM_TEST_CGROUP_PARENT")
	job := Untracked("AXIOM_TEST_JOB")
	return func(req *testcontainers.GenericContainerRequest) error {
		if job != "" {
			if err := testcontainers.WithLabels(map[string]string{jobLabel: job})(req); err != nil {
				return err
			}
		}
		return testcontainers.WithHostConfigModifier(func(host *container.HostConfig) {
			if parent != "" {
				host.CgroupParent = parent
			}
		})(req)
	}
}
