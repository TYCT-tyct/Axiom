// Package containertest holds the host policy shared by test containers.
package containertest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
)

// jobLabel carries AXIOM_TEST_JOB; scripts/ci/clean-test-containers.sh removes the containers that have it.
const jobLabel = "axiom.ci.job"

// A container's ports are published on loopback at ports from firstHostPort to lastHostPort, which this package picks.
// Left to pick, Docker takes one from the kernel's ephemeral range (Linux 32768-60999, Windows 49152-65535), where the
// kernel also places every outbound connection and bind(0) of every process on the host: on a shared CI host those hold
// enough of the range that Docker's few tries on consecutive ports all failed with "address already in use" (main run
// 42604281494, 2026-11-09). Below the range a port is held only by a process that asked for that number, so a port
// that is free a moment before Docker binds it stays free unless another test picked the same one; Start then tries
// another. scripts/ci/publish.sh does the same for the CI scripts.
const (
	firstHostPort = 20000
	lastHostPort  = 29999
)

// startAttempts bounds Start. A try fails only when another process took the port between the check and Docker's bind,
// a chance of about the share of the 10,000 ports in use at once, so eight in a row do not fail on any host this runs on.
const startAttempts = 8

// WithHostPolicy keeps test services inside the CI host's resource budget, labels them for cleanup, and publishes each
// of the request's exposed ports on 127.0.0.1 at a port it picks below the ephemeral range. Unset or empty variables
// preserve the developer's configuration. Apply it last: it must see the request's exposed ports and any
// container-specific host configuration. A container that exposes no port gets the image's ports published by Docker
// in the ephemeral range, so name them. ctx bounds the check that a port is free. Start the container with Start, which
// tries another port when the bind loses.
func WithHostPolicy(ctx context.Context) testcontainers.CustomizeRequestOption {
	parent := os.Getenv("AXIOM_TEST_CGROUP_PARENT")
	job := Untracked("AXIOM_TEST_JOB")
	return func(req *testcontainers.GenericContainerRequest) error {
		if job != "" {
			if err := testcontainers.WithLabels(map[string]string{jobLabel: job})(req); err != nil {
				return err
			}
		}
		bindings, err := loopbackBindings(ctx, req.ExposedPorts)
		if err != nil {
			return err
		}
		return testcontainers.WithHostConfigModifier(func(host *container.HostConfig) {
			if parent != "" {
				host.CgroupParent = parent
			}
			host.PortBindings = bindings
		})(req)
	}
}

// loopbackBindings publishes each exposed port once, on 127.0.0.1 at an unused port below the ephemeral range.
func loopbackBindings(ctx context.Context, exposed []string) (network.PortMap, error) {
	bindings := network.PortMap{}
	for _, spec := range exposed {
		port, err := network.ParsePort(spec)
		if err != nil {
			return nil, fmt.Errorf("exposed port %q: %w", spec, err)
		}
		if _, ok := bindings[port]; ok {
			continue
		}
		host, err := unusedPort(ctx)
		if err != nil {
			return nil, err
		}
		bindings[port] = []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: host}}
	}
	return bindings, nil
}

// unusedPort is a port from firstHostPort to lastHostPort that nothing on this machine listens on now. The check also
// skips the ports Windows reserves for Hyper-V, which refuse a bind.
func unusedPort(ctx context.Context) (string, error) {
	for range 100 {
		port := strconv.Itoa(firstHostPort + rand.IntN(lastHostPort-firstHostPort+1)) //nolint:gosec // a port to try, not a secret
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
		if err == nil {
			return port, listener.Close()
		}
	}
	return "", fmt.Errorf("no unused port from %d to %d", firstHostPort, lastHostPort)
}

// Start runs start, which builds its container with WithHostPolicy, again while Docker cannot bind a port the policy
// picked, at most startAttempts times; each try picks new ports. A container that failed to start is removed, so a
// caller gets either a started container or an error.
func Start[C testcontainers.Container](ctx context.Context, start func() (C, error)) (C, error) {
	for attempt := 1; ; attempt++ {
		c, err := start()
		if err == nil {
			return c, nil
		}
		var zero C
		if removeErr := testcontainers.TerminateContainer(c); removeErr != nil {
			return zero, errors.Join(err, fmt.Errorf("remove the container that did not start: %w", removeErr))
		}
		if attempt == startAttempts || ctx.Err() != nil || !portTaken(err) {
			return zero, err
		}
		slog.Warn("containertest: a try lost its host port, trying others", "try", attempt, "of", startAttempts, "err", err)
	}
}

// portTaken reports Docker's refusal to bind a published port: "address already in use" when another process holds it,
// "port is already allocated" when another container does, both under this prefix.
func portTaken(err error) bool {
	return strings.Contains(err.Error(), "programming external connectivity")
}
