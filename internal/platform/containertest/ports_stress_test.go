package containertest

import (
	"context"
	"math/rand/v2"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"golang.org/x/sync/errgroup"
)

// stressFree is how many ports of the ephemeral range the churn leaves free: enough for the test's own connections to
// the containers, few enough that Docker's own pick of ten consecutive ports often finds none.
const stressFree = 100

// TestStartUnderPortChurn reproduces the CI failure of 2026-11-09 and shows that Start survives it: while other sockets
// hold and churn most of the ephemeral range, containers whose port Docker picks fail to start with "driver failed
// programming external connectivity", and containers started with Start and WithHostPolicy all start. It holds the
// whole range but stressFree ports, so it runs only where the range is small and its own: scripts/ci/port-stress.sh
// runs it in a Docker-in-Docker container, and AXIOM_PORT_STRESS is how many containers each half starts.
func TestStartUnderPortChurn(t *testing.T) {
	count, err := strconv.Atoi(os.Getenv("AXIOM_PORT_STRESS"))
	if err != nil || count <= 0 {
		t.Skip("AXIOM_PORT_STRESS is not set: run scripts/ci/port-stress.sh")
	}
	first, last := ephemeralRange(t)
	if last-first+1 > 20*stressFree {
		t.Fatalf("the ephemeral range %d-%d is a host's, not this test's own: run scripts/ci/port-stress.sh", first, last)
	}
	churn(t, last-first+1-stressFree)

	dockerPicks := startMany(t, count, func(ctx context.Context) (*testcontainers.DockerContainer, error) {
		return testcontainers.Run(ctx, "nginx:alpine", testcontainers.WithExposedPorts("80/tcp"))
	})
	policyPicks := startMany(t, count, func(ctx context.Context) (*testcontainers.DockerContainer, error) {
		return Start(ctx, func() (*testcontainers.DockerContainer, error) {
			return testcontainers.Run(ctx, "nginx:alpine", testcontainers.WithExposedPorts("80/tcp"), WithHostPolicy(ctx))
		})
	})
	t.Logf("of %d containers each, %d lost their port when Docker picked it and %d with Start", count, dockerPicks, policyPicks)
	if dockerPicks == 0 {
		t.Errorf("no container whose port Docker picked failed: the churn did not reproduce the failure")
	}
	if policyPicks != 0 {
		t.Errorf("%d containers started with Start lost their port", policyPicks)
	}
}

func ephemeralRange(t *testing.T) (int, int) {
	t.Helper()
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data))
	first, err1 := strconv.Atoi(fields[0])
	last, err2 := strconv.Atoi(fields[1])
	if err1 != nil || err2 != nil {
		t.Fatalf("ip_local_port_range = %q", data)
	}
	return first, last
}

// churn holds up to hold ports of the ephemeral range by listening on port 0, as outbound connections and other
// processes on a shared host do, and keeps replacing them until the test ends.
func churn(t *testing.T, hold int) {
	t.Helper()
	held := make([]net.Listener, 0, hold)
	for range hold {
		held = listenEphemeral(t, held)
	}
	t.Logf("holding %d ephemeral ports", len(held))
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-done:
				release(t, held...)
				return
			default:
			}
			i := rand.IntN(len(held)) //nolint:gosec // which port to replace, not a secret
			release(t, held[i])
			held = listenEphemeral(t, slices.Delete(held, i, i+1))
		}
	})
	t.Cleanup(func() { close(done); wg.Wait() })
}

// listenEphemeral adds a listener on a port the kernel picks from the ephemeral range, when one is left.
func listenEphemeral(t *testing.T, held []net.Listener) []net.Listener {
	if l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0"); err == nil {
		return append(held, l)
	}
	return held
}

func release(t *testing.T, listeners ...net.Listener) {
	for _, l := range listeners {
		if err := l.Close(); err != nil {
			t.Errorf("release a held port: %v", err)
		}
	}
}

// startMany starts count containers four at a time, removes each, and returns how many lost their host port.
func startMany(t *testing.T, count int, start func(context.Context) (*testcontainers.DockerContainer, error)) int {
	t.Helper()
	var lost atomic.Int64
	var group errgroup.Group
	group.SetLimit(4)
	for range count {
		group.Go(func() error {
			c, err := start(t.Context())
			if removeErr := testcontainers.TerminateContainer(c); removeErr != nil {
				t.Errorf("remove a container: %v", removeErr)
			}
			switch {
			case err == nil:
			case portTaken(err):
				lost.Add(1)
			default:
				t.Errorf("start a container: %v", err)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		t.Error(err)
	}
	return int(lost.Load())
}
