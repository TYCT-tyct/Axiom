package containertest

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"axiom/internal/platform/jsonx"
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
	const startupDeadline = 2 * time.Minute
	ctx := t.Context()
	container, err := Start(ctx, func() (*testcontainers.DockerContainer, error) {
		return testcontainers.Run(ctx, SeaweedFSImage,
			testcontainers.WithExposedPorts("8333/tcp", "9333/tcp", "8888/tcp"),
			testcontainers.WithFiles(testcontainers.ContainerFile{
				Reader:            strings.NewReader(seaweedfsS3Config),
				ContainerFilePath: "/tmp/s3.json",
				FileMode:          0o444,
			}),
			testcontainers.WithCmd("server", "-dir=/data", "-s3", "-s3.port=8333", "-s3.config=/tmp/s3.json"),
			testcontainers.WithWaitStrategyAndDeadline(startupDeadline,
				wait.ForListeningPort("8333/tcp"),
				wait.ForHTTP("/cluster/status").WithPort("9333/tcp").WithForcedIPv4LocalHost().
					WithStartupTimeout(startupDeadline).WithResponseMatcher(seaweedFSMasterReady),
				wait.ForHTTP("/").WithPort("8888/tcp").WithForcedIPv4LocalHost().WithStartupTimeout(startupDeadline),
			),
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
	filer, err := container.MappedPort(ctx, "8888/tcp")
	if err != nil {
		t.Fatalf("find the SeaweedFS filer port: %v", err)
	}
	for _, bucket := range buckets {
		createBucket(t, ctx, "http://"+net.JoinHostPort(host, filer.Port()), bucket)
	}
	return SeaweedFS{
		Endpoint:         "http://" + net.JoinHostPort(host, port.Port()),
		InternalEndpoint: "http://" + net.JoinHostPort(ip, "8333"),
	}
}

// The master can serve cluster status before electing the leader weed shell needs.
func seaweedFSMasterReady(body io.Reader) bool {
	var status struct {
		IsLeader bool
		Leader   string
	}
	return jsonx.Decode(body, &status) == nil && status.IsLeader && status.Leader != ""
}

// createBucket creates one bucket as a directory under the filer's /buckets, which is what a bucket is to SeaweedFS's S3
// server. It goes to the filer, which the wait strategy saw answer, and not through weed shell: the shell finds the filer
// through the master, which lists it only once the filer has registered, a moment after the S3 port opens; without one
// the shell prints the error and still exits 0, and its handshake with the master stalled past a deadline on a busy CI
// host (run 45910331015).
func createBucket(t testing.TB, ctx context.Context, filer, name string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, filer+"/buckets/"+name+"/", nil)
	if err != nil {
		t.Fatalf("create bucket %q: %v", name, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create bucket %q: %v", name, err)
	}
	body, readErr := io.ReadAll(resp.Body)
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("create bucket %q: %v", name, err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create bucket %q: %s, %v: %s", name, resp.Status, readErr, body)
	}
}
