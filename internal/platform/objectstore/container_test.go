package objectstore_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"axiom/internal/platform/containertest"
	"axiom/internal/platform/objectstore"
	"axiom/internal/platform/secret"
)

// The bucket server the tests run against: the implementation docs/architecture.md chose for a self-hosted bucket.
const seaweedfsImage = "chrislusf/seaweedfs:3.80"

// seaweedfsS3Config gives the client the one credential pair the test uses, with the actions a bucket needs.
const seaweedfsS3Config = `{"identities":[{"name":"axiom","credentials":[{"accessKey":"AKIAOBJECTSTORETEST","secretKey":"objectstore-test-secret"}],"actions":["Admin","Read","Write","List","Tagging"]}]}`

// TestSeaweedFS runs the client against a real S3 server, because a hand-written signer can pass its own fake and
// still disagree with an implementation about escaping, headers or list pagination.
//
//nolint:cyclop,gocognit // one end-to-end flow: start, create, write, read, list, delete
func TestSeaweedFS(t *testing.T) {
	ctx := t.Context()
	container, err := testcontainers.Run(ctx, seaweedfsImage,
		testcontainers.WithExposedPorts("8333/tcp"),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader:            strings.NewReader(seaweedfsS3Config),
			ContainerFilePath: "/tmp/s3.json",
			FileMode:          0o444,
		}),
		testcontainers.WithCmd("server", "-dir=/data", "-s3", "-s3.port=8333", "-s3.config=/tmp/s3.json"),
		testcontainers.WithWaitStrategyAndDeadline(2*time.Minute, wait.ForListeningPort("8333/tcp")),
		containertest.WithHostPolicy(),
	)
	if err != nil {
		t.Fatalf("start SeaweedFS: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := testcontainers.TerminateContainer(container, testcontainers.StopContext(stopCtx)); err != nil {
			t.Errorf("remove the SeaweedFS container: %v", err)
		}
	})
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
	createBucket(t, ctx, container, "axiom")
	client, err := objectstore.Open(objectstore.Config{
		Endpoint: "http://" + net.JoinHostPort(host, port.Port()), Bucket: "axiom", Region: "us-east-1",
		AccessKeyID: "AKIAOBJECTSTORETEST", SecretAccessKey: secret.New("objectstore-test-secret"), PathStyle: true,
	})
	if err != nil {
		t.Fatalf("open the client: %v", err)
	}
	t.Cleanup(client.Close)

	opCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := client.Check(opCtx); err != nil {
		t.Fatalf("check the bucket: %v", err)
	}
	// One mebibyte with bytes that are not XML or UTF-8 friendly, a key with a space and a slash, and a content type.
	body := bytes.Repeat([]byte{0x00, 0xff, 0x80, 'a', '\n'}, 1<<18)
	key := "archive/2026/a b.txt"
	info, err := client.Put(opCtx, objectstore.Upload{Key: key, Body: bytes.NewReader(body), Size: int64(len(body)), ContentType: "text/plain"})
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if info.Size != int64(len(body)) || info.ETag == "" {
		t.Fatalf("put returned %+v", info)
	}
	// Above the multipart threshold: a real server has to accept the initiate, part and complete requests, not just the
	// single PUT that a fake can imitate.
	large := bytes.Repeat(body, 20)
	largeKey := "archive/2026/large.bin"
	if info, err := client.Put(opCtx, objectstore.Upload{Key: largeKey, Body: bytes.NewReader(large), Size: int64(len(large))}); err != nil || info.Size != int64(len(large)) {
		t.Fatalf("multipart put = %+v, %v", info, err)
	}
	largeObject, err := client.Get(opCtx, largeKey)
	if err != nil {
		t.Fatalf("get the multipart object: %v", err)
	}
	largeGot, largeReadErr := io.ReadAll(largeObject.Body)
	largeCloseErr := largeObject.Body.Close()
	if largeReadErr != nil || largeCloseErr != nil {
		t.Fatalf("read the multipart object: %v, %v", largeReadErr, largeCloseErr)
	}
	if !bytes.Equal(largeGot, large) {
		t.Fatalf("read %d multipart bytes that differ from the %d written", len(largeGot), len(large))
	}
	object, err := client.Get(opCtx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, readErr := io.ReadAll(object.Body)
	closeErr := object.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read the object: %v, %v", readErr, closeErr)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("read %d bytes that differ from the %d written", len(got), len(body))
	}
	head, err := client.Head(opCtx, key)
	if err != nil || head.Size != int64(len(body)) {
		t.Fatalf("head = %+v, %v", head, err)
	}
	// A non-ASCII key: the signature must escape its UTF-8 bytes the way S3 does, not double-encode or normalize them.
	unicodeKey := "archive/中文.txt"
	if _, err := client.Put(opCtx, objectstore.Upload{Key: unicodeKey, Body: strings.NewReader("unicode"), Size: 7}); err != nil {
		t.Fatalf("put unicode key: %v", err)
	}
	page, err := client.List(opCtx, objectstore.ListOptions{Prefix: "archive/"})
	if err != nil || len(page.Objects) != 3 {
		t.Fatalf("list = %+v, %v", page, err)
	}
	seen := map[string]bool{}
	for _, object := range page.Objects {
		seen[object.Key] = true
	}
	if !seen[key] || !seen[unicodeKey] || !seen[largeKey] {
		t.Fatalf("list keys = %v, want %q, %q and %q", seen, key, unicodeKey, largeKey)
	}
	if err := client.Delete(opCtx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := client.Head(opCtx, key); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("head after delete = %v, want ErrNotFound", err)
	}
	if err := client.Delete(opCtx, unicodeKey); err != nil {
		t.Fatalf("delete unicode key: %v", err)
	}
	if err := client.Delete(opCtx, largeKey); err != nil {
		t.Fatalf("delete the multipart key: %v", err)
	}
}

// createBucket creates the one bucket the test uses. SeaweedFS's S3 server needs it before a PUT; the shell talks to
// the master the same process started.
func createBucket(t *testing.T, ctx context.Context, container *testcontainers.DockerContainer, name string) {
	t.Helper()
	execCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	code, out, err := container.Exec(execCtx, []string{
		"sh", "-ec", fmt.Sprintf("echo 's3.bucket.create -name %s' | weed shell -master=localhost:9333", name),
	})
	if err != nil {
		t.Fatalf("create bucket %q: %v", name, err)
	}
	if output, readErr := io.ReadAll(out); readErr != nil || code != 0 {
		t.Fatalf("create bucket %q: exit %d, %v: %s", name, code, readErr, output)
	}
}
