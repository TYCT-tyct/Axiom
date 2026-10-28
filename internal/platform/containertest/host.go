// Package containertest holds the host policy shared by test containers.
package containertest

import (
	"os"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
)

// WithHostPolicy keeps test services inside the CI host's resource budget and labels them for cleanup. Unset or
// empty variables preserve the developer's configuration. Apply it after any container-specific host configuration.
func WithHostPolicy() testcontainers.CustomizeRequestOption {
	parent := os.Getenv("AXIOM_TEST_CGROUP_PARENT")
	job := ""
	// The job changes every run but is not a test input. Getenv/LookupEnv would include it in Go's test cache key.
	for _, entry := range os.Environ() {
		if value, ok := strings.CutPrefix(entry, "AXIOM_TEST_JOB="); ok {
			job = value
		}
	}
	return func(req *testcontainers.GenericContainerRequest) error {
		if job != "" {
			if err := testcontainers.WithLabels(map[string]string{"axiom.ci.job": job})(req); err != nil {
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
