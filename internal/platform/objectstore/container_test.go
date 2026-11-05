package objectstore_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/containertest"
	"axiom/internal/platform/objectstore"
	"axiom/internal/platform/secret"
)

// checkBucketCreation asserts a new bucket is created and an existing one is not.
func checkBucketCreation(t *testing.T, ctx context.Context, client *objectstore.Client) {
	t.Helper()
	if created, err := client.Create(ctx, false); err != nil || !created {
		t.Fatalf("create the bucket = %v, %v; want true, nil", created, err)
	}
	if created, err := client.Create(ctx, true); err != nil || created {
		t.Fatalf("create the existing bucket = %v, %v; want false, nil", created, err)
	}
}

// getAndCompare reads key back and asserts it equals want.
func getAndCompare(t *testing.T, ctx context.Context, client *objectstore.Client, key string, want []byte) {
	t.Helper()
	object, err := client.Get(ctx, key)
	if err != nil {
		t.Fatalf("get %q: %v", key, err)
	}
	got, readErr := io.ReadAll(object.Body)
	closeErr := object.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read %q: %v, %v", key, readErr, closeErr)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%q read %d bytes that differ from the %d written", key, len(got), len(want))
	}
}

// checkMultipart asserts a body above the multipart threshold goes through initiate, part and complete, and reads back.
func checkMultipart(t *testing.T, ctx context.Context, client *objectstore.Client, body []byte) {
	t.Helper()
	large := bytes.Repeat(body, 20)
	key := "archive/2026/large.bin"
	if info, err := client.Put(ctx, objectstore.Upload{Key: key, Body: bytes.NewReader(large), Size: int64(len(large))}); err != nil || info.Size != int64(len(large)) {
		t.Fatalf("multipart put = %+v, %v", info, err)
	}
	getAndCompare(t, ctx, client, key, large)
}

// checkListing asserts every key written is in the list.
func checkListing(t *testing.T, ctx context.Context, client *objectstore.Client, keys ...string) {
	t.Helper()
	page, err := client.List(ctx, objectstore.ListOptions{Prefix: "archive/"})
	if err != nil || len(page.Objects) != len(keys) {
		t.Fatalf("list = %+v, %v", page, err)
	}
	seen := map[string]bool{}
	for _, object := range page.Objects {
		seen[object.Key] = true
	}
	for _, key := range keys {
		if !seen[key] {
			t.Fatalf("list keys = %v, want %q", seen, key)
		}
	}
}

// TestSeaweedFS runs the client against a real S3 server, because a hand-written signer can pass its own fake and
// still disagree with an implementation about escaping, headers or list pagination.
func TestSeaweedFS(t *testing.T) {
	ctx := t.Context()
	s3 := containertest.StartSeaweedFS(t, "axiom")
	client, err := objectstore.Open(objectstore.Config{
		Endpoint: s3.Endpoint, Bucket: "axiom", Region: "us-east-1",
		AccessKeyID: containertest.SeaweedFSAccessKey, SecretAccessKey: secret.New(containertest.SeaweedFSSecretKey), PathStyle: true,
	})
	if err != nil {
		t.Fatalf("open the client: %v", err)
	}
	t.Cleanup(client.Close)

	opCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	creating, err := objectstore.Open(objectstore.Config{
		Endpoint: s3.Endpoint, Bucket: "axiom-created", Region: "us-east-1",
		AccessKeyID: containertest.SeaweedFSAccessKey, SecretAccessKey: secret.New(containertest.SeaweedFSSecretKey), PathStyle: true,
	})
	if err != nil {
		t.Fatalf("open the creating client: %v", err)
	}
	defer creating.Close()
	checkBucketCreation(t, opCtx, creating)
	if err := client.Check(opCtx); err != nil {
		t.Fatalf("check the bucket: %v", err)
	}
	// A mebibyte and a quarter with bytes that are not XML or UTF-8 friendly, a key with a space and a slash, and a content type.
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
	checkMultipart(t, opCtx, client, body)
	getAndCompare(t, opCtx, client, key, body)
	checkHead(t, opCtx, client, key, int64(len(body)))
	// A non-ASCII key: the signature must escape its UTF-8 bytes the way S3 does, not double-encode or normalize them.
	unicodeKey := putUnicode(t, opCtx, client)
	checkListing(t, opCtx, client, key, unicodeKey, "archive/2026/large.bin")
	checkDeleted(t, opCtx, client, key)
	if err := client.Delete(opCtx, unicodeKey); err != nil {
		t.Fatalf("delete unicode key: %v", err)
	}
	if err := client.Delete(opCtx, "archive/2026/large.bin"); err != nil {
		t.Fatalf("delete the multipart key: %v", err)
	}
}

// checkHead asserts the stored object's size.
func checkHead(t *testing.T, ctx context.Context, client *objectstore.Client, key string, size int64) {
	t.Helper()
	head, err := client.Head(ctx, key)
	if err != nil || head.Size != size {
		t.Fatalf("head %q = %+v, %v", key, head, err)
	}
}

// putUnicode writes a non-ASCII key and returns it.
func putUnicode(t *testing.T, ctx context.Context, client *objectstore.Client) string {
	t.Helper()
	unicodeKey := "archive/中文.txt"
	if _, err := client.Put(ctx, objectstore.Upload{Key: unicodeKey, Body: strings.NewReader("unicode"), Size: 7}); err != nil {
		t.Fatalf("put unicode key: %v", err)
	}
	return unicodeKey
}

// checkDeleted deletes key and asserts a head now reports ErrNotFound.
func checkDeleted(t *testing.T, ctx context.Context, client *objectstore.Client, key string) {
	t.Helper()
	if err := client.Delete(ctx, key); err != nil {
		t.Fatalf("delete %q: %v", key, err)
	}
	if _, err := client.Head(ctx, key); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("head after delete = %v, want ErrNotFound", err)
	}
}
