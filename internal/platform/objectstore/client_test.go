package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/secret"
)

// fakeS3 is the smallest S3 the client's tests need: one bucket, PUT/GET/HEAD/DELETE, a two-page listing, and switches
// for the failures a test wants the client to meet.
type fakeS3 struct {
	mu        sync.Mutex
	objects   map[string][]byte
	paths     []string
	failures  int  // answer 503 this many times, then behave
	forbidden bool // answer 403
	redirect  bool // answer 307
}

// writeTest is a test handler's write: the client on the other end failing is the test's own failure to report.
func writeTest(w io.Writer, value string) {
	_, _ = io.WriteString(w, value) //nolint:errcheck // a test handler's write
}

func newFakeS3() *fakeS3 { return &fakeS3{objects: map[string][]byte{}} }

// seen copies the paths the server answered, under its lock, so a test reads no field the handler is writing.
func (f *fakeS3) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func (f *fakeS3) body(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.objects[key])
}

//nolint:cyclop // a tiny S3 double: one branch per method and failure switch
func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
	switch {
	case f.failures > 0:
		f.failures--
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	case f.forbidden:
		w.WriteHeader(http.StatusForbidden)
		return
	case f.redirect:
		w.Header().Set("Location", "http://example.invalid/elsewhere")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/axiom")
	key = strings.TrimPrefix(key, "/")
	if key == "" && r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.URL.Query().Get("list-type") == "2" {
		f.list(w, r)
		return
	}
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.objects[key] = body
		w.Header().Set("ETag", `"`+sumHex(body)+`"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet, http.MethodHead:
		body, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			writeTest(w, `<Error><Code>NoSuchKey</Code><Message>missing</Message><RequestId>r1</RequestId></Error>`)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("ETag", `"`+sumHex(body)+`"`)
		w.Header().Set("Last-Modified", time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Format(http.TimeFormat))
		if r.Method == http.MethodGet {
			writeTest(w, string(body))
		}
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (*fakeS3) list(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("continuation-token") == "" {
		writeTest(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult><Name>axiom</Name><IsTruncated>true</IsTruncated>
<NextContinuationToken>page2</NextContinuationToken>
<Contents><Key>a.txt</Key><Size>3</Size><ETag>"x"</ETag><LastModified>2026-01-02T03:04:05Z</LastModified></Contents>
</ListBucketResult>`)
		return
	}
	writeTest(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult><Name>axiom</Name><IsTruncated>false</IsTruncated>
<Contents><Key>b.txt</Key><Size>4</Size><ETag>"y"</ETag><LastModified>2026-01-02T03:04:06Z</LastModified></Contents>
</ListBucketResult>`)
}

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

func TestPutRefusesAnObjectOverTheSinglePutLimit(t *testing.T) {
	client := newTestClient(t, newFakeS3())
	_, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("v"), Size: maxObjectSize + 1})
	if err == nil {
		t.Fatal("a body over the single PUT limit was accepted")
	}
}

func TestRetryAfter(t *testing.T) {
	for _, c := range []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"seconds", "2", 2 * time.Second},
		{"zero", "0", 0},
		{"date", time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat), 3 * time.Second},
		{"past date", time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat), 0},
		{"nonsense", "soon", 0},
		{"cap", "3600", retryAfterCap},
		{"huge", "9223372036854775807", retryAfterCap},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{"Retry-After": {c.header}}}
			got := retryAfter(resp)
			if c.name == "date" {
				if got < 2*time.Second || got > 3*time.Second {
					t.Fatalf("retryAfter = %v, want about 3s", got)
				}
				return
			}
			if got != c.want {
				t.Fatalf("retryAfter = %v, want %v", got, c.want)
			}
		})
	}
}
