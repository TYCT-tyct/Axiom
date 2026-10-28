package kvtest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"axiom/internal/platform/containertest"
)

// box is one Valkey container, started as a deployment starts it: the ACL file is copied in before the server starts,
// because testcontainers copies a file into a container before it starts and the server must read the file at startup.
type box struct {
	container *testcontainers.DockerContainer
	address   string
}

// stopGrace is how long a stopped container's server has to exit: a second, because a test's server holds nothing that
// a clean shutdown saves.
const stopGrace = time.Second

// startServer runs a container with the given ACL file and settings, and returns it once it accepts connections.
func startServer(ctx context.Context, users string, settings options) (*box, error) {
	container, err := testcontainers.Run(ctx, image,
		testcontainers.WithExposedPorts(port),
		testcontainers.WithHostConfigModifier(func(host *container.HostConfig) {
			if settings.hostPort != "" {
				host.PortBindings = network.PortMap{network.MustParsePort(port): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: settings.hostPort}}}
			}
		}),
		containertest.WithHostPolicy(),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader: strings.NewReader(users), ContainerFilePath: aclPath, FileMode: 0o444,
		}),
		testcontainers.WithCmd("valkey-server", "--aclfile", aclPath, "--save", "", "--appendonly", "no",
			"--maxmemory", settings.memory, "--maxmemory-policy", settings.policy),
		testcontainers.WithWaitStrategyAndDeadline(startTimeout,
			wait.ForLog("Ready to accept connections"), wait.ForListeningPort(port)))
	if err != nil {
		return nil, err
	}
	b := &box{container: container}
	host, err := container.Host(ctx)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("find the valkey container: %w", err), b.terminate(context.WithoutCancel(ctx)))
	}
	mapped, err := container.MappedPort(ctx, port)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("find the valkey port: %w", err), b.terminate(context.WithoutCancel(ctx)))
	}
	if host == "localhost" {
		host = "127.0.0.1" // as in pgtest: Docker Desktop's IPv6 forwarder stalls under load
	}
	b.address = net.JoinHostPort(host, mapped.Port())
	return b, nil
}

// terminate removes the container and everything in it.
func (b *box) terminate(ctx context.Context) error {
	return testcontainers.TerminateContainer(b.container, testcontainers.StopContext(ctx))
}

// stop halts the server and keeps the container, so that start brings the same server back: same port, same ACL file,
// and an empty dataset, which is what a restart of a deployment is (README「不持久化」).
func (b *box) stop(ctx context.Context) error {
	grace := stopGrace
	return b.container.Stop(ctx, &grace)
}

// start starts the container that stop halted.
func (b *box) start(ctx context.Context) error {
	return b.container.Start(ctx)
}
