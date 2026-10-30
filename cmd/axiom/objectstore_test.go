package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"axiom/internal/platform/objectstore"
)

// commandStore is the one bucket the command's test needs. Its answers are the smallest valid S3 responses for the
// verbs the command runs.
type commandStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	bucket  bool
	created int
}

// writeTest is a test handler's write: the client on the other end failing is the test's own failure to report.
func writeTest(w io.Writer, value string) {
	_, _ = io.WriteString(w, value) //nolint:errcheck // a test handler's write
}

func (s *commandStore) createdCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.created
}

func (s *commandStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/axiom"), "/")
	if r.Method == http.MethodHead && key == "" {
		if !s.bucket {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method == http.MethodPut && key == "" {
		s.created++
		s.bucket = true
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.URL.Query().Get("list-type") == "2" {
		writeTest(w, `<ListBucketResult><IsTruncated>false</IsTruncated>`)
		for name, body := range s.objects {
			writeTest(w, "<Contents><Key>"+name+"</Key><Size>"+strconv.Itoa(len(body))+"</Size><ETag>\"e\"</ETag><LastModified>2026-01-02T03:04:05Z</LastModified></Contents>")
		}
		writeTest(w, `</ListBucketResult>`)
		return
	}
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.objects[key] = body
		w.Header().Set("ETag", `"e"`)
	case http.MethodGet, http.MethodHead:
		body, ok := s.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			writeTest(w, `<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set("ETag", `"e"`)
		if r.Method == http.MethodGet {
			writeTest(w, string(body))
		}
	case http.MethodDelete:
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func commandEnv(server string) []string {
	return []string{
		"AXIOM_OBJECTSTORE_ENDPOINT=" + server,
		"AXIOM_OBJECTSTORE_BUCKET=axiom",
		"AXIOM_OBJECTSTORE_ACCESS_KEY_ID=AKIAEXAMPLE",
		"AXIOM_OBJECTSTORE_SECRET_ACCESS_KEY=secret",
	}
}

type recordingPutter struct {
	deadline time.Time
	has      bool
}

func (p *recordingPutter) Put(ctx context.Context, upload objectstore.Upload) (objectstore.ObjectInfo, error) {
	p.deadline, p.has = ctx.Deadline()
	return objectstore.ObjectInfo{Key: upload.Key, Size: upload.Size, ETag: `"e"`}, nil
}

type recordingGetter struct {
	deadline time.Time
	has      bool
}

func (g *recordingGetter) Get(ctx context.Context, key string) (*objectstore.Object, error) {
	g.deadline, g.has = ctx.Deadline()
	return &objectstore.Object{
		ObjectInfo: objectstore.ObjectInfo{Key: key, Size: 1, ETag: `"e"`},
		Body:       io.NopCloser(strings.NewReader("x")),
	}, nil
}

func TestObjectstoreTransfersUseTheTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		upload := filepath.Join(dir, "upload.bin")
		if err := os.WriteFile(upload, []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
		putter := &recordingPutter{}
		if err := putObject(t.Context(), putter, "p", upload, "", 2*time.Hour, io.Discard); err != nil {
			t.Fatalf("put: %v", err)
		}
		if !putter.has || putter.deadline.Sub(time.Now()) != 2*time.Hour {
			t.Fatalf("put deadline = %v, want 2h from now", putter.deadline)
		}
		getter := &recordingGetter{}
		dest := filepath.Join(dir, "downloaded")
		if err := getObject(t.Context(), getter, "g", dest, 2*time.Hour, io.Discard); err != nil {
			t.Fatalf("get: %v", err)
		}
		if !getter.has || getter.deadline.Sub(time.Now()) != 2*time.Hour {
			t.Fatalf("get deadline = %v, want 2h from now", getter.deadline)
		}
		if got, err := os.ReadFile(dest); err != nil || string(got) != "x" {
			t.Fatalf("downloaded %q, %v", got, err)
		}
		ctx, cancel := transferContext(t.Context(), time.Hour)
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("transferContext made no deadline")
		}
		cancel()
		<-ctx.Done()
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("canceled context error = %v", ctx.Err())
		}
	})
}

func TestObjectstoreCommandRejectsNonPositiveTransferTimeout(t *testing.T) {
	store := &commandStore{bucket: true}
	server := httptest.NewServer(store)
	defer server.Close()
	err := objectstoreCommand(t.Context(), commandEnv(server.URL), []string{"put", "--timeout", "0"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("--timeout 0 = %v, want a refusal", err)
	}
}

func TestObjectstoreCommandMovesAnObject(t *testing.T) {
	store := &commandStore{objects: map[string][]byte{}, bucket: true}
	server := httptest.NewServer(store)
	defer server.Close()
	environ := commandEnv(server.URL)
	dir := t.TempDir()
	upload := filepath.Join(dir, "upload.bin")
	content := []byte("archive bytes")
	if err := os.WriteFile(upload, content, 0o600); err != nil {
		t.Fatal(err)
	}
	download := filepath.Join(dir, "download.bin")

	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := objectstoreCommand(t.Context(), environ, args, &out); err != nil {
			t.Fatalf("objectstore %v: %v", args, err)
		}
		return out.String()
	}
	if got := run("check"); !strings.Contains(got, `"bucket":"axiom"`) || !strings.Contains(got, `"checked":true`) {
		t.Fatalf("check printed %q", got)
	}
	if got := run("put", "--key", "a/b.bin", "--file", upload, "--content-type", "application/octet-stream"); !strings.Contains(got, `"size":13`) {
		t.Fatalf("put printed %q", got)
	}
	if got := run("list", "--prefix", "a/"); !strings.Contains(got, `"key":"a/b.bin"`) {
		t.Fatalf("list printed %q", got)
	}
	run("get", "--key", "a/b.bin", "--file", download)
	got, err := os.ReadFile(download) //nolint:gosec // a path this test wrote under t.TempDir()
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("downloaded %q, %v", got, err)
	}
	run("delete", "--key", "a/b.bin")
	var out bytes.Buffer
	err = objectstoreCommand(context.Background(), environ, []string{"get", "--key", "a/b.bin", "--file", filepath.Join(dir, "missing.bin")}, &out)
	if !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}

func TestObjectstoreCommandCreatesListsNamesAndDeletesAPrefix(t *testing.T) {
	store := &commandStore{objects: map[string][]byte{}}
	server := httptest.NewServer(store)
	defer server.Close()
	environ := commandEnv(server.URL)
	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := objectstoreCommand(t.Context(), environ, args, &out); err != nil {
			t.Fatalf("objectstore %v: %v", args, err)
		}
		return out.String()
	}
	if got := run("create"); !strings.Contains(got, `"created":true`) {
		t.Fatalf("create printed %q", got)
	}
	if got := store.createdCount(); got != 1 {
		t.Fatalf("the server saw %d bucket creations, want 1", got)
	}
	if err := objectstoreCommand(t.Context(), environ, []string{"create"}, io.Discard); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("create on an existing bucket = %v, want a refusal", err)
	}
	if got := run("create", "--allow-existing"); !strings.Contains(got, `"created":false`) {
		t.Fatalf("create --allow-existing printed %q", got)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "a")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("put", "--key", "sets/one/a", "--file", file)
	if got := run("list", "--names", "--prefix", "sets/"); got != "sets/one/a\n" {
		t.Fatalf("list --names printed %q", got)
	}
	if got := run("delete", "--prefix", "sets/"); !strings.Contains(got, `"deleted":1`) {
		t.Fatalf("delete --prefix printed %q", got)
	}
	if got := run("list", "--names", "--prefix", "sets/"); got != "" {
		t.Fatalf("list after delete printed %q", got)
	}
	for _, args := range [][]string{
		{"delete"},
		{"delete", "--key", "k", "--prefix", "p/"},
	} {
		var out bytes.Buffer
		if err := objectstoreCommand(t.Context(), environ, args, &out); err == nil {
			t.Fatalf("objectstore %v was accepted", args)
		}
	}
}

func TestObjectstoreCommandReportsConfigurationProblems(t *testing.T) {
	for _, c := range []struct {
		name    string
		environ []string
		want    string
	}{
		{"unconfigured", nil, "no bucket is configured"},
		{"half configured", []string{"AXIOM_OBJECTSTORE_ENDPOINT=https://s3.example.com"}, "AXIOM_OBJECTSTORE_BUCKET: required"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := objectstoreCommand(t.Context(), c.environ, []string{"check"}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestObjectstoreCommandUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"put", "--key", "k"}} {
		if err := objectstoreCommand(context.Background(), nil, args, io.Discard); err == nil {
			t.Fatalf("objectstore %v was accepted", args)
		}
	}
}
