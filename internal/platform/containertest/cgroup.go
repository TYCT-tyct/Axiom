// Package containertest holds the host policy shared by test containers.
package containertest

import (
	"os"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
)

// WithCgroupParent keeps test services inside the CI host's resource budget. An unset or empty variable leaves the
// developer's Docker configuration alone. Apply it after any container-specific host configuration.
func WithCgroupParent() testcontainers.CustomizeRequestOption {
	parent := os.Getenv("AXIOM_TEST_CGROUP_PARENT")
	return testcontainers.WithHostConfigModifier(func(host *container.HostConfig) {
		if parent != "" {
			host.CgroupParent = parent
		}
	})
}
