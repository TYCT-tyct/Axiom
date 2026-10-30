package objectstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/secret"
)

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := Open(Config{
		Endpoint: server.URL, Bucket: "axiom", Region: "us-east-1",
		AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: secret.New("secret"), PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.retryBase = time.Millisecond
	client.retryMax = 2 * time.Millisecond
	t.Cleanup(client.Close)
	return client
}

func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestCheckSignsTheEmptyPayload(t *testing.T) {
	headers := make(chan http.Header, 1)
	client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	client.now = func() time.Time { return signedAt }
	if err := client.Check(deadline(t)); err != nil {
		t.Fatalf("Check: %v", err)
	}
	got := <-headers
	if hash := got.Get("X-Amz-Content-Sha256"); hash != emptySHA256 {
		t.Fatalf("check sent payload hash %q, want the empty payload's %q", hash, emptySHA256)
	}
	want, err := client.request(deadline(t), request{method: http.MethodHead, payloadHash: emptySHA256})
	if err != nil {
		t.Fatal(err)
	}
	if auth := got.Get("Authorization"); auth != want.Header.Get("Authorization") {
		t.Fatalf("check signed\n%s\nwant\n%s", auth, want.Header.Get("Authorization"))
	}
}

func TestEveryOperationNeedsADeadline(t *testing.T) {
	fake := newFakeS3()
	client := newTestClient(t, fake)
	ops := map[string]func(context.Context) error{
		"check": client.Check,
		"put": func(ctx context.Context) error {
			_, err := client.Put(ctx, Upload{Key: "k", Body: strings.NewReader("v"), Size: 1})
			return err
		},
		"get":    func(ctx context.Context) error { _, err := client.Get(ctx, "k"); return err },
		"head":   func(ctx context.Context) error { _, err := client.Head(ctx, "k"); return err },
		"delete": func(ctx context.Context) error { return client.Delete(ctx, "k") },
		"list":   func(ctx context.Context) error { _, err := client.List(ctx, ListOptions{}); return err },
	}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			if err := op(context.Background()); !errors.Is(err, ErrDeadlineRequired) {
				t.Fatalf("err = %v, want ErrDeadlineRequired", err)
			}
		})
	}
	if len(fake.seen()) != 0 {
		t.Fatalf("a request without a deadline reached the server: %v", fake.seen())
	}
}

//nolint:cyclop // one object through every verb is the point of the test
func TestRoundTrip(t *testing.T) {
	fake := newFakeS3()
	fake.bucket = true
	client := newTestClient(t, fake)
	ctx := deadline(t)

	if err := client.Check(ctx); err != nil {
		t.Fatalf("Check: %v", err)
	}
	info, err := client.Put(ctx, Upload{Key: "archive/x.txt", Body: strings.NewReader("hello"), Size: 5, ContentType: "text/plain"})
	if err != nil || info.Size != 5 {
		t.Fatalf("Put = %+v, %v", info, err)
	}
	head, err := client.Head(ctx, "archive/x.txt")
	if err != nil || head.Size != 5 || head.ETag == "" {
		t.Fatalf("Head = %+v, %v", head, err)
	}
	object, err := client.Get(ctx, "archive/x.txt")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	body, readErr := io.ReadAll(object.Body)
	closeErr := object.Body.Close()
	if readErr != nil || closeErr != nil || string(body) != "hello" {
		t.Fatalf("Get body = %q, %v, %v", body, readErr, closeErr)
	}
	page, err := client.List(ctx, ListOptions{Prefix: "archive/"})
	if err != nil || len(page.Objects) != 1 || page.Objects[0].Key != "a.txt" || page.NextToken != "page2" {
		t.Fatalf("List = %+v, %v", page, err)
	}
	if err := client.Delete(ctx, "archive/x.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := client.Head(ctx, "archive/x.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Head after Delete = %v, want ErrNotFound", err)
	}
}

func TestPutRetriesAndSendsTheSameBytes(t *testing.T) {
	fake := newFakeS3()
	fake.failures = 2
	client := newTestClient(t, fake)
	if _, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("hello"), Size: 5}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := len(fake.seen()); got != 3 {
		t.Fatalf("Put made %d attempts, want 3", got)
	}
	if got := fake.body("k"); got != "hello" {
		t.Fatalf("stored %q, want hello", got)
	}
}

func TestRetriesStopAtTheAttemptLimit(t *testing.T) {
	fake := newFakeS3()
	fake.failures = 10
	client := newTestClient(t, fake)
	_, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("hello"), Size: 5})
	var remote *Error
	if !errors.As(err, &remote) || remote.Status != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want the last 503 as an Error", err)
	}
	if got := len(fake.seen()); got != maxAttempts {
		t.Fatalf("Put made %d attempts, want %d", got, maxAttempts)
	}
}

func TestAForbiddenAnswerIsNotRetried(t *testing.T) {
	fake := newFakeS3()
	fake.forbidden = true
	client := newTestClient(t, fake)
	_, err := client.Get(deadline(t), "k")
	var remote *Error
	if !errors.As(err, &remote) || remote.Status != http.StatusForbidden {
		t.Fatalf("err = %v, want a 403 Error", err)
	}
	if len(fake.seen()) != 1 {
		t.Fatalf("the client made %d attempts, want 1", len(fake.seen()))
	}
}

func TestARedirectIsNotFollowed(t *testing.T) {
	fake := newFakeS3()
	fake.redirect = true
	client := newTestClient(t, fake)
	_, err := client.Get(deadline(t), "k")
	var remote *Error
	if !errors.As(err, &remote) || remote.Status != http.StatusTemporaryRedirect {
		t.Fatalf("err = %v, want the 307 as an Error", err)
	}
	if len(fake.seen()) != 1 {
		t.Fatalf("the redirect was followed: %v", fake.seen())
	}
}

func TestListFollowsTheServerPages(t *testing.T) {
	client := newTestClient(t, newFakeS3())
	ctx := deadline(t)
	first, err := client.List(ctx, ListOptions{Prefix: "archive/"})
	if err != nil || len(first.Objects) != 1 || first.NextToken != "page2" {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	second, err := client.List(ctx, ListOptions{Prefix: "archive/", ContinuationToken: first.NextToken})
	if err != nil || len(second.Objects) != 1 || second.Objects[0].Key != "b.txt" || second.NextToken != "" {
		t.Fatalf("second page = %+v, %v", second, err)
	}
}

func TestPutStartsAtTheBeginningAndReusesTheReader(t *testing.T) {
	fake := newFakeS3()
	client := newTestClient(t, fake)
	reader := bytes.NewReader([]byte("hello"))
	if _, err := reader.Read(make([]byte, 2)); err != nil { // leave a caller's reader at a nonzero offset
		t.Fatal(err)
	}
	for range 2 {
		info, err := client.Put(deadline(t), Upload{Key: "k", Body: reader, Size: 5})
		if err != nil || info.Size != 5 {
			t.Fatalf("Put = %+v, %v", info, err)
		}
		if got := fake.body("k"); got != "hello" {
			t.Fatalf("stored %q, want hello from the start of the reader", got)
		}
	}
	if _, err := reader.Seek(2, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	info, err := client.Put(deadline(t), Upload{Key: "k", Body: reader, Size: -1})
	if err != nil || info.Size != 5 {
		t.Fatalf("Put with unknown size = %+v, %v", info, err)
	}
	if got := fake.body("k"); got != "hello" {
		t.Fatalf("stored %q, want hello with an unknown size too", got)
	}
}

func TestPutWithACanceledContextReadsNothing(t *testing.T) {
	reader := &countingSeeker{reader: bytes.NewReader([]byte("hello"))}
	client := newTestClient(t, newFakeS3())
	ctx, cancel := context.WithCancel(deadline(t))
	cancel()
	if _, err := client.Put(ctx, Upload{Key: "k", Body: reader, Size: -1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if reader.reads != 0 {
		t.Fatalf("a canceled Put read the body %d times", reader.reads)
	}
}

func TestHashBodyStopsAtTheLimitPlusOneByte(t *testing.T) {
	reader := &endlessReader{}
	_, _, err := hashBody(deadline(t), reader, 64)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if reader.read != 65 {
		t.Fatalf("read %d bytes, want the limit plus one", reader.read)
	}
}

// countingSeeker counts read calls so a test can prove a canceled operation never touched the body.
type countingSeeker struct {
	reader *bytes.Reader
	reads  int
}

func (c *countingSeeker) Read(p []byte) (int, error) {
	c.reads++
	return c.reader.Read(p)
}

func (c *countingSeeker) Seek(offset int64, whence int) (int64, error) {
	return c.reader.Seek(offset, whence)
}

// endlessReader produces bytes forever and counts them; only the client's limit can stop it.
type endlessReader struct{ read int64 }

func (e *endlessReader) Read(p []byte) (int, error) {
	clear(p)
	e.read += int64(len(p))
	return len(p), nil
}

func (*endlessReader) Seek(int64, int) (int64, error) { return 0, nil }

func TestPutRefusesASizeThatDoesNotMatch(t *testing.T) {
	client := newTestClient(t, newFakeS3())
	if _, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("hello"), Size: 4}); err == nil {
		t.Fatal("a wrong Size was accepted")
	}
}

func TestUnsafeKeysAreRefused(t *testing.T) {
	client := newTestClient(t, newFakeS3())
	for _, key := range []string{"", "/x", "a/../b", "a/./b", "a//b", "a\x00b"} {
		if _, err := client.Put(deadline(t), Upload{Key: key, Body: strings.NewReader("v"), Size: 1}); err == nil {
			t.Fatalf("key %q was accepted", key)
		}
	}
}

func TestPutRefusesAnObjectOverTheObjectLimit(t *testing.T) {
	client := newTestClient(t, newFakeS3())
	_, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("v"), Size: maxObjectSize + 1})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestCreateBucketIsIdempotent(t *testing.T) {
	fake := newFakeS3()
	client := newTestClient(t, fake)
	created, err := client.Create(deadline(t), false)
	if err != nil || !created {
		t.Fatalf("Create = %v, %v; want a new bucket", created, err)
	}
	if _, err := client.Create(deadline(t), false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Create on an existing bucket without consent = %v, want a refusal", err)
	}
	created, err = client.Create(deadline(t), true)
	if err != nil || created {
		t.Fatalf("Create on an existing bucket = %v, %v; want false, nil", created, err)
	}
	if got := fake.createdCount(); got != 1 {
		t.Fatalf("created %d times, want one PUT and a HEAD on the second call", got)
	}
}

func TestCreateAcceptsAnOwnedBucketFromTheConflictCode(t *testing.T) {
	fake := newFakeS3()
	fake.bucket = true
	fake.headMissing = true // HEAD lost a race with the bucket's creation; the PUT sees it
	client := newTestClient(t, fake)
	created, err := client.Create(deadline(t), true)
	if err != nil || created {
		t.Fatalf("Create on a bucket owned by these credentials = %v, %v; want false, nil", created, err)
	}
	if got := fake.createdCount(); got != 1 {
		t.Fatalf("created %d times, want one PUT", got)
	}
}

func TestCreateRejectsABucketAnotherOwnerHas(t *testing.T) {
	fake := newFakeS3()
	fake.bucket = true
	fake.headMissing = true // the PUT is the only place the conflict code is visible
	fake.bucketConflict = "BucketAlreadyExists"
	client := newTestClient(t, fake)
	_, err := client.Create(deadline(t), false)
	if err == nil || !strings.Contains(err.Error(), "BucketAlreadyExists") {
		t.Fatalf("Create on another owner's bucket = %v, want the conflict refused", err)
	}
}

func TestCreateSendsTheRegionOutsideUsEast(t *testing.T) {
	fake := newFakeS3()
	client := newTestClient(t, fake)
	client.region = "eu-west-1"
	created, err := client.Create(deadline(t), false)
	if err != nil || !created {
		t.Fatalf("Create = %v, %v; want a new bucket", created, err)
	}
	body := fake.creationBody()
	if !strings.Contains(body, "<LocationConstraint>eu-west-1</LocationConstraint>") {
		t.Fatalf("create body = %q, want the region's LocationConstraint", body)
	}
	if !strings.Contains(body, `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`) {
		t.Fatalf("create body = %q, want the S3 namespace", body)
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, c := range []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"seconds", "2", 2 * time.Second},
		{"zero", "0", 0},
		{"date", now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second},
		{"past date", now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{"nonsense", "soon", 0},
		{"cap", "3600", retryAfterCap},
		{"huge", "9223372036854775807", retryAfterCap},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{"Retry-After": {c.header}}}
			if got := retryAfter(resp, now); got != c.want {
				t.Fatalf("retryAfter = %v, want %v", got, c.want)
			}
		})
	}
}
