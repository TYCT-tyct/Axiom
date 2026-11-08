package objectstore

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
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

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
	if f.inject(w) {
		return
	}
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/axiom"), "/")
	query := r.URL.Query()
	if key == "" && f.bucketVerb(w, r) {
		return
	}
	if query.Get("list-type") == "2" {
		f.list(w, r)
		return
	}
	if _, initiating := query["uploads"]; initiating && r.Method == http.MethodPost {
		f.initiate(w)
		return
	}
	if uploadID := query.Get("uploadId"); uploadID != "" {
		f.multipart(w, r, key, uploadID)
		return
	}
	f.object(w, r, key)
}

// inject answers the failure a test asked for and reports whether it did.
func (f *fakeS3) inject(w http.ResponseWriter) bool {
	switch {
	case f.failures > 0:
		f.failures--
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusServiceUnavailable)
		return true
	case f.forbidden:
		w.WriteHeader(http.StatusForbidden)
		return true
	case f.redirect:
		w.Header().Set("Location", "http://example.invalid/elsewhere")
		w.WriteHeader(http.StatusTemporaryRedirect)
		return true
	}
	return false
}

// bucketVerb answers the bucket's own PUT and HEAD and reports whether the request was one of them.
func (f *fakeS3) bucketVerb(w http.ResponseWriter, r *http.Request) bool {
	switch r.Method {
	case http.MethodPut:
		f.createBucket(w, r)
	case http.MethodHead:
		f.headBucket(w)
	default:
		return false
	}
	return true
}

func (f *fakeS3) createBucket(w http.ResponseWriter, r *http.Request) {
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
}

func (f *fakeS3) headBucket(w http.ResponseWriter) {
	if !f.bucket || f.headMissing {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (f *fakeS3) initiate(w http.ResponseWriter) {
	id := fmt.Sprintf("upload-%d", len(f.uploads)+1)
	f.uploads[id] = map[int][]byte{}
	writeTest(w, `<InitiateMultipartUploadResult><UploadId>`+id+`</UploadId></InitiateMultipartUploadResult>`)
}

func (f *fakeS3) object(w http.ResponseWriter, r *http.Request, key string) {
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if _, taken := f.objects[key]; taken && r.Header.Get("If-None-Match") == "*" {
			w.WriteHeader(http.StatusPreconditionFailed)
			writeTest(w, "<Error><Code>PreconditionFailed</Code></Error>")
			return
		}
		f.objects[key] = body
		w.Header().Set("ETag", `"`+sumHex(body)+`"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodGet, http.MethodHead:
		f.readObject(w, r, key)
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeS3) readObject(w http.ResponseWriter, r *http.Request, key string) {
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
}

// multipart answers one part's PUT, the completion and the abort, under the handler's lock.
func (f *fakeS3) multipart(w http.ResponseWriter, r *http.Request, key, uploadID string) {
	parts, ok := f.uploads[uploadID]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		writeTest(w, `<Error><Code>NoSuchUpload</Code><Message>missing upload</Message></Error>`)
		return
	}
	switch r.Method {
	case http.MethodPut:
		f.uploadPart(w, r, parts)
	case http.MethodPost:
		f.completeUpload(w, r, key, uploadID, parts)
	case http.MethodDelete:
		delete(f.uploads, uploadID)
		f.aborted = append(f.aborted, uploadID)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *fakeS3) uploadPart(w http.ResponseWriter, r *http.Request, parts map[int][]byte) {
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
}

func (f *fakeS3) completeUpload(w http.ResponseWriter, r *http.Request, key, uploadID string, parts map[int][]byte) {
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
		Parts []multipartPart `xml:"Part"`
	}
	if err := xml.Unmarshal(body, &request); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	object, err := joinParts(parts, request.Parts)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.objects[key] = object
	delete(f.uploads, uploadID)
	writeTest(w, `<CompleteMultipartUploadResult><ETag>"complete"</ETag></CompleteMultipartUploadResult>`)
}

// joinParts lays the parts out in the order the completion names, as S3 does.
func joinParts(parts map[int][]byte, order []multipartPart) ([]byte, error) {
	var object []byte
	for _, part := range order {
		data, ok := parts[part.PartNumber]
		if !ok {
			return nil, fmt.Errorf("part %d is missing", part.PartNumber)
		}
		object = append(object, data...)
	}
	return object, nil
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
