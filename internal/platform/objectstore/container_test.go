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

// TestSeaweedFS runs the client against a real S3 server, because a hand-written signer can pass its own fake and
// still disagree with an implementation about escaping, headers or list pagination.
//
//nolint:cyclop,gocognit // one end-to-end flow: start, create, write, read, list, delete
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
	if created, err := creating.Create(opCtx, false); err != nil || !created {
		t.Fatalf("create the bucket = %v, %v; want true, nil", created, err)
	}
	if created, err := creating.Create(opCtx, true); err != nil || created {
		t.Fatalf("create the existing bucket = %v, %v; want false, nil", created, err)
	}
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
