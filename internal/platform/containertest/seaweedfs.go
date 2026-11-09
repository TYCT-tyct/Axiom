package containertest

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// SeaweedFSImage is the self-hosted S3 server the tests run against; docs/architecture.md chose it for a self-hosted
// bucket. It is a test input, not a deployment dependency.
const SeaweedFSImage = "chrislusf/seaweedfs:3.80"

// The one credential pair the test server accepts, with the actions a bucket needs.
const (
	SeaweedFSAccessKey = "AKIAOBJECTSTORETEST"
	SeaweedFSSecretKey = "objectstore-test-secret"
)

var seaweedfsS3Config = fmt.Sprintf(`{"identities":[{"name":"axiom","credentials":[{"accessKey":%q,"secretKey":%q}],"actions":["Admin","Read","Write","List","Tagging"]}]}`, SeaweedFSAccessKey, SeaweedFSSecretKey)

// SeaweedFS is a running bucket server with the requested buckets created.
type SeaweedFS struct {
	// Endpoint is the S3 API as this process reaches it, through the host's published port.
	Endpoint string
	// InternalEndpoint is the S3 API as another container on the default bridge network reaches it. A container that
	// has to read or write the bucket uses this one.
	InternalEndpoint string
}

// StartSeaweedFS runs the S3 server and creates buckets, and removes the container when the test ends. The server is on
// the default bridge network, so a container started by the same test can reach InternalEndpoint.
func StartSeaweedFS(t testing.TB, buckets ...string) SeaweedFS {
	t.Helper()
	ctx := t.Context()
	container, err := Start(ctx, func() (*testcontainers.DockerContainer, error) {
		return testcontainers.Run(ctx, SeaweedFSImage,
			testcontainers.WithExposedPorts("8333/tcp"),
			testcontainers.WithFiles(testcontainers.ContainerFile{
				Reader:            strings.NewReader(seaweedfsS3Config),
				ContainerFilePath: "/tmp/s3.json",
				FileMode:          0o444,
			}),
			testcontainers.WithCmd("server", "-dir=/data", "-s3", "-s3.port=8333", "-s3.config=/tmp/s3.json"),
			testcontainers.WithWaitStrategyAndDeadline(2*time.Minute, wait.ForListeningPort("8333/tcp")),
			WithHostPolicy(ctx),
		)
	})
	if err != nil {
		t.Fatalf("start SeaweedFS: %v", err)
	}
	Cleanup(t, container, "SeaweedFS")
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("find the SeaweedFS host: %v", err)
	}
	port, err := container.MappedPort(ctx, "8333/tcp")
	if err != nil {
		t.Fatalf("find the SeaweedFS port: %v", err)
	}
	if host == "localhost" {
		host = "127.0.0.1" // Docker Desktop's IPv6 forwarder stalls under load
	}
	ip, err := container.ContainerIP(ctx)
	if err != nil {
		t.Fatalf("find the SeaweedFS bridge address: %v", err)
	}
	for _, bucket := range buckets {
		createBucket(t, ctx, container, bucket)
	}
	return SeaweedFS{
		Endpoint:         "http://" + net.JoinHostPort(host, port.Port()),
		InternalEndpoint: "http://" + net.JoinHostPort(ip, "8333"),
	}
}

// createBucket creates one bucket. SeaweedFS's S3 server needs it before a PUT; the shell talks to the master the same
// process started, over IPv4 because Docker Desktop resolves localhost to the IPv6 socket the master does not listen on.
func createBucket(t testing.TB, ctx context.Context, container *testcontainers.DockerContainer, name string) {
	t.Helper()
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	code, out, err := container.Exec(execCtx, []string{
		"sh", "-ec", fmt.Sprintf("echo 's3.bucket.create -name %s' | weed shell -master=127.0.0.1:9333", name),
	})
	if err != nil {
		t.Fatalf("create bucket %q: %v", name, err)
	}
	if output, readErr := io.ReadAll(out); readErr != nil || code != 0 {
		t.Fatalf("create bucket %q: exit %d, %v: %s", name, code, readErr, output)
	}
}
