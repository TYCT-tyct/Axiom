package objectstore

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/secret"
)

// fakeS3 is the smallest S3 the client's tests need: one bucket, PUT/GET/HEAD/DELETE, a two-page listing, multipart
// uploads, and switches for the failures a test wants the client to meet.
type fakeS3 struct {
	mu             sync.Mutex
	objects        map[string][]byte
	uploads        map[string]map[int][]byte
	aborted        []string
	paths          []string
	created        int
	bucket         bool   // the bucket exists
	headMissing    bool   // the bucket exists but HEAD says 404, as an eventual-consistency race
	bucketConflict string // the code a 409 PUT carries; empty means BucketAlreadyOwnedByYou
	createBody     string // the body of the last successful PUT
	failures       int    // answer 503 this many times, then behave
	failPartAt     int    // answer 500 for this part number, forever
	failDone       bool   // answer 500 to every completion
	errorDoneLeft  int    // answer 200 with an Error document this many times
	noETag         bool   // answer 200 with a completion result that has no ETag
	forbidden      bool   // answer 403
	redirect       bool   // answer 307
}

// writeTest is a test handler's write: the client on the other end failing is the test's own failure to report.
func writeTest(w io.Writer, value string) {
	_, _ = io.WriteString(w, value) //nolint:errcheck // a test handler's write
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string][]byte{}, uploads: map[string]map[int][]byte{}}
}

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

func (f *fakeS3) createdCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created
}

func (f *fakeS3) creationBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createBody
}

func (f *fakeS3) abortCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.aborted)
}

func (f *fakeS3) aborts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.aborted...)
}

func (f *fakeS3) uploadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.uploads)
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
	query := r.URL.Query()
	if key == "" && r.Method == http.MethodPut {
		f.created++
		if f.bucket || f.headMissing {
			code := f.bucketConflict
			if code == "" {
				code = "BucketAlreadyOwnedByYou"
			}
			w.WriteHeader(http.StatusConflict)
			writeTest(w, "<Error><Code>"+code+"</Code></Error>")
			return
		}
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // a test double reading its own request
		f.createBody = string(body)
		f.bucket = true
		w.WriteHeader(http.StatusOK)
		return
	}
	if key == "" && r.Method == http.MethodHead {
		if !f.bucket || f.headMissing {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if query.Get("list-type") == "2" {
		f.list(w, r)
		return
	}
	if _, initiating := query["uploads"]; initiating && r.Method == http.MethodPost {
		id := fmt.Sprintf("upload-%d", len(f.uploads)+1)
		f.uploads[id] = map[int][]byte{}
		writeTest(w, `<InitiateMultipartUploadResult><UploadId>`+id+`</UploadId></InitiateMultipartUploadResult>`)
		return
	}
	if uploadID := query.Get("uploadId"); uploadID != "" {
		f.multipart(w, r, uploadID)
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

// multipart answers one part's PUT, the completion and the abort, under the handler's lock.
func (f *fakeS3) multipart(w http.ResponseWriter, r *http.Request, uploadID string) {
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/axiom"), "/")
	parts, ok := f.uploads[uploadID]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		writeTest(w, `<Error><Code>NoSuchUpload</Code><Message>missing upload</Message></Error>`)
		return
	}
	switch r.Method {
	case http.MethodPut:
		number, err := strconv.Atoi(r.URL.Query().Get("partNumber"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if number == f.failPartAt {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		parts[number] = body
		w.Header().Set("ETag", fmt.Sprintf(`"part-%d-%s"`, number, sumHex(body)))
		w.WriteHeader(http.StatusOK)
	case http.MethodPost:
		if f.failDone {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if f.errorDoneLeft > 0 {
			f.errorDoneLeft--
			writeTest(w, `<Error><Code>InternalError</Code><Message>completion failed</Message><RequestId>r1</RequestId></Error>`)
			return
		}
		if f.noETag {
			writeTest(w, `<CompleteMultipartUploadResult><ETag></ETag></CompleteMultipartUploadResult>`)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var request struct {
			Parts []struct {
				PartNumber int    `xml:"PartNumber"`
				ETag       string `xml:"ETag"`
			} `xml:"Part"`
		}
		if err := xml.Unmarshal(body, &request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var object []byte
		for _, part := range request.Parts {
			data, ok := parts[part.PartNumber]
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			object = append(object, data...)
		}
		f.objects[key] = object
		delete(f.uploads, uploadID)
		writeTest(w, `<CompleteMultipartUploadResult><ETag>"complete"</ETag></CompleteMultipartUploadResult>`)
	case http.MethodDelete:
		delete(f.uploads, uploadID)
		f.aborted = append(f.aborted, uploadID)
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

// multipartClient makes a ten-byte object three parts: the threshold is eight bytes and a part four, so the test does
// not move sixteen mebibytes through the double.
func multipartClient(t *testing.T, fake *fakeS3) *Client {
	t.Helper()
	client := newTestClient(t, fake)
	client.threshold = 8
	client.partSize = 4
	return client
}

func TestPutAboveTheThresholdUsesParts(t *testing.T) {
	fake := newFakeS3()
	client := multipartClient(t, fake)
	info, err := client.Put(deadline(t), Upload{Key: "archive.bin", Body: strings.NewReader("abcdefghij"), Size: 10})
	if err != nil || info.Size != 10 || info.ETag != `"complete"` {
		t.Fatalf("Put = %+v, %v", info, err)
	}
	if got := fake.body("archive.bin"); got != "abcdefghij" {
		t.Fatalf("stored %q, want abcdefghij", got)
	}
	if fake.uploadCount() != 0 {
		t.Fatalf("the upload survived completion")
	}
	paths := strings.Join(fake.seen(), "\n")
	for _, want := range []string{"?uploads=", "partNumber=1&uploadId=upload-1", "partNumber=3&uploadId=upload-1", "uploadId=upload-1"} {
		if !strings.Contains(paths, want) {
			t.Fatalf("requests lack %q:\n%s", want, paths)
		}
	}
}

func TestPutAtTheThresholdStaysOneRequest(t *testing.T) {
	fake := newFakeS3()
	client := multipartClient(t, fake)
	if _, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("abcdefgh"), Size: 8}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if strings.Contains(strings.Join(fake.seen(), "\n"), "uploads") {
		t.Fatalf("a body at the threshold started a multipart upload: %v", fake.seen())
	}
}

func TestMultipartAbortsWhenAPartKeepsFailing(t *testing.T) {
	fake := newFakeS3()
	fake.failPartAt = 2
	client := multipartClient(t, fake)
	_, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("abcdefghij"), Size: 10})
	if err == nil {
		t.Fatal("a failing part was accepted")
	}
	if got := fake.aborts(); len(got) != 1 || got[0] != "upload-1" {
		t.Fatalf("aborted = %v, want upload-1", got)
	}
	if fake.uploadCount() != 0 {
		t.Fatalf("the failed upload survived")
	}
	partAttempts := 0
	for _, path := range fake.seen() {
		if strings.Contains(path, "partNumber=2") {
			partAttempts++
		}
	}
	if partAttempts != maxAttempts {
		t.Fatalf("part 2 was attempted %d times, want %d", partAttempts, maxAttempts)
	}
}

func TestMultipartAbortsWhenCompletionFails(t *testing.T) {
	fake := newFakeS3()
	fake.failDone = true
	client := multipartClient(t, fake)
	if _, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("abcdefghij"), Size: 10}); err == nil {
		t.Fatal("a failing completion was accepted")
	}
	if got := fake.aborts(); len(got) != 1 {
		t.Fatalf("aborted = %v, want one abort", got)
	}
}

func TestMultipartRejectsATwoHundredErrorDocument(t *testing.T) {
	fake := newFakeS3()
	fake.errorDoneLeft = 10 // more than the client attempts
	client := multipartClient(t, fake)
	_, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("abcdefghij"), Size: 10})
	var remote *Error
	if !errors.As(err, &remote) || remote.Code != "InternalError" {
		t.Fatalf("err = %v, want the 200 Error document", err)
	}
	if got := fake.aborts(); len(got) != 1 {
		t.Fatalf("aborted = %v, want one abort", got)
	}
}

func TestMultipartRetriesA200ErrorDocument(t *testing.T) {
	fake := newFakeS3()
	fake.errorDoneLeft = 1
	client := multipartClient(t, fake)
	info, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("abcdefghij"), Size: 10})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if info.ETag != `"complete"` || fake.body("k") != "abcdefghij" {
		t.Fatalf("completed %+v with %q", info, fake.body("k"))
	}
	if fake.abortCount() != 0 {
		t.Fatalf("aborted a retry that succeeded")
	}
}

func TestMultipartRejectsACompletionWithoutAnETag(t *testing.T) {
	fake := newFakeS3()
	fake.noETag = true
	client := multipartClient(t, fake)
	if _, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("abcdefghij"), Size: 10}); err == nil {
		t.Fatal("a completion without an ETag was accepted")
	}
	if fake.abortCount() == 0 {
		t.Fatal("the failed upload was not aborted")
	}
}

func TestMultipartRefusesASizeThatDoesNotMatchBeforeUploading(t *testing.T) {
	fake := newFakeS3()
	client := multipartClient(t, fake)
	if _, err := client.Put(deadline(t), Upload{Key: "k", Body: strings.NewReader("abcdefghij"), Size: 9}); err == nil {
		t.Fatal("a wrong Size was accepted")
	}
	if len(fake.seen()) != 0 {
		t.Fatalf("a mismatch reached the server: %v", fake.seen())
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
