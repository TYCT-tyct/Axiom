package httpserver

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// writeTo sends s, failing the test if the client is gone.
func writeTo(t *testing.T, w http.ResponseWriter, s string) {
	t.Helper()
	if _, err := io.WriteString(w, s); err != nil {
		t.Error(err)
	}
}

// answer is a handler that sets extra headers and writes body in chunks of the given size, as contentType.
func answer(t *testing.T, contentType, body string, chunk int, extra http.Header) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		maps.Copy(w.Header(), extra)
		w.Header().Set("Content-Type", contentType)
		for rest := body; rest != ""; {
			n := min(chunk, len(rest))
			writeTo(t, w, rest[:n])
			rest = rest[n:]
		}
	})
}

// fetch makes a request that names its encodings itself, so nothing decompresses behind the test's back.
func fetch(t *testing.T, h http.Handler, method, acceptEncoding string) (reply, []byte) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, "/", nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return reply{rec.Code, rec.Header()}, rec.Body.Bytes()
}

func gunzip(t *testing.T, data []byte) string {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestCompress: JSON of at least compressMin bytes is gzipped for a client that accepts it, whole or written in small
// pieces; everything else, a small body, another type, an unwilling client, a response already encoded or with a
// validator, goes out as the handler wrote it.
func TestCompress(t *testing.T) {
	big := `{"data":"` + strings.Repeat("a", 4000) + `"}`
	small := `{"data":"a"}`
	atMin, belowMin := strings.Repeat("a", compressMin), strings.Repeat("a", compressMin-1)
	events := strings.Repeat("data: x\n\n", 500)
	cases := []struct {
		name, accept, contentType, body string
		chunk                           int
		extra                           http.Header
		wantGzip                        bool
	}{
		{"a large answer", "gzip", "application/json", big, len(big), nil, true},
		{"written in small pieces", "gzip, br", "application/json", big, 100, nil, true},
		{"a +json type with a charset", "*", "application/problem+json; charset=utf-8", big, len(big), nil, true},
		{"just over the minimum", "gzip", "application/json", atMin, 100, nil, true},
		{"just under the minimum", "gzip", "application/json", belowMin, 100, nil, false},
		{"a small answer", "gzip", "application/json", small, len(small), nil, false},
		{"a client that takes no gzip", "br", "application/json", big, len(big), nil, false},
		{"a client that refuses gzip", "gzip;q=0", "application/json", big, len(big), nil, false},
		{"a client that names no encoding", "", "application/json", big, len(big), nil, false},
		{"a stream", "gzip", "text/event-stream", events, 100, nil, false},
		{"newline-delimited JSON", "gzip", "application/x-ndjson", events, 100, nil, false},
		{"text", "gzip", "text/plain", big, len(big), nil, false},
		{"a body already encoded", "gzip", "application/json", big, len(big), http.Header{"Content-Encoding": {"br"}}, false},
		{"a body with a validator", "gzip", "application/json", big, len(big), http.Header{"Etag": {`"v1"`}}, false},
	}
	for _, c := range cases {
		resp, body := fetch(t, Compress(answer(t, c.contentType, c.body, c.chunk, c.extra)), http.MethodGet, c.accept)
		if resp.Header.Get("Vary") != "Accept-Encoding" {
			t.Errorf("%s: Vary %q", c.name, resp.Header.Get("Vary"))
		}
		switch {
		case c.wantGzip && (resp.Header.Get("Content-Encoding") != "gzip" || gunzip(t, body) != c.body):
			t.Errorf("%s: encoding %q, %d bytes: not the gzip of the answer", c.name, resp.Header.Get("Content-Encoding"), len(body))
		case !c.wantGzip && (resp.Header.Get("Content-Encoding") != c.extra.Get("Content-Encoding") || string(body) != c.body):
			t.Errorf("%s: changed: encoding %q, %d bytes of %d", c.name, resp.Header.Get("Content-Encoding"), len(body), len(c.body))
		}
	}
}

// TestCompressLeavesWhatItShould: HEAD, an empty answer, a status without a body and an interim response pass.
func TestCompressLeavesWhatItShould(t *testing.T) {
	big := strings.Repeat("a", 4000)
	// The recorder keeps the body a server would drop from a HEAD answer; what matters is that it is not encoded.
	if resp, _ := fetch(t, Compress(answer(t, "application/json", big, 4000, nil)), http.MethodHead, "gzip"); resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("HEAD: encoded as %q", resp.Header.Get("Content-Encoding"))
	}
	statuses := map[string]struct {
		status int
		h      http.Handler
	}{
		"no content":    {204, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })},
		"not modified":  {304, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotModified) })},
		"teapot, empty": {418, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })},
		"nothing":       {200, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})},
	}
	for name, c := range statuses {
		resp, body := fetch(t, Compress(c.h), http.MethodGet, "gzip")
		if resp.StatusCode != c.status || len(body) != 0 || resp.Header.Get("Content-Encoding") != "" {
			t.Errorf("%s: %d %q (%d bytes)", name, resp.StatusCode, resp.Header.Get("Content-Encoding"), len(body))
		}
	}
	// The status the handler chose survives compression.
	created := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		writeTo(t, w, big)
	})
	if resp, body := fetch(t, Compress(created), http.MethodGet, "gzip"); resp.StatusCode != http.StatusCreated || gunzip(t, body) != big {
		t.Errorf("created: %d", resp.StatusCode)
	}
}

// streamTo serves h and returns the response to a request that accepts gzip, for the caller to read while the handler
// is still writing.
func streamTo(t *testing.T, h http.Handler) (reply, io.Reader) {
	t.Helper()
	server := httptest.NewServer(Compress(h))
	t.Cleanup(server.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip") // named by hand, so the client leaves the body encoded
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := resp.Body.Close(); err != nil {
			t.Error(err)
		}
	})
	return reply{resp.StatusCode, resp.Header}, resp.Body
}

// flushThen writes first, flushes it, and writes second only once the client has heard the first. A compressor
// that held the first back would deadlock the pair, and the handler gives up after five seconds.
func flushThen(t *testing.T, contentType, first, second string, heard <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		writeTo(t, w, first)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
		}
		select {
		case <-heard:
		case <-time.After(5 * time.Second):
			t.Error("the first part was held back")
		}
		writeTo(t, w, second)
	})
}

// TestCompressDoesNotHoldBackAStream: a handler that flushes is heard at once, whatever its type, and a stream is
// never encoded.
func TestCompressDoesNotHoldBackAStream(t *testing.T) {
	for _, contentType := range []string{"text/event-stream", "application/json"} {
		heard := make(chan struct{})
		resp, body := streamTo(t, flushThen(t, contentType, "data: first\n\n", "data: second\n\n", heard))
		reader := bufio.NewReader(body)
		if line, err := reader.ReadString('\n'); err != nil || line != "data: first\n" {
			t.Fatalf("%s: first line %q: %v", contentType, line, err)
		}
		close(heard)
		rest, err := io.ReadAll(reader)
		if err != nil || string(rest) != "\ndata: second\n\n" || resp.Header.Get("Content-Encoding") != "" {
			t.Errorf("%s: the rest %q (%v), encoding %q", contentType, rest, err, resp.Header.Get("Content-Encoding"))
		}
	}
}

// TestCompressFlushesWhatItCompresses: a large JSON body that is flushed part way reaches the client up to that
// point, and the whole body decodes.
func TestCompressFlushesWhatItCompresses(t *testing.T) {
	first, second := strings.Repeat("a", 3000), strings.Repeat("b", 3000)
	heard := make(chan struct{})
	resp, body := streamTo(t, flushThen(t, "application/json", first, second, heard))
	zr, err := gzip.NewReader(body)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(first))
	if _, err := io.ReadFull(zr, got); err != nil || string(got) != first {
		t.Fatalf("the flushed part: %v", err)
	}
	close(heard)
	rest, err := io.ReadAll(zr)
	if err != nil || string(rest) != second || resp.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("the rest: %d bytes (%v), encoding %q", len(rest), err, resp.Header.Get("Content-Encoding"))
	}
}

// panicking answers with the start of a JSON body and panics, flushing first if flush, behind Public and Compress.
func panicking(t *testing.T, flush bool) http.Handler {
	return Public(Compress(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeTo(t, w, `{"partial":`)
		if flush {
			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Error(err)
			}
		}
		panic("planted")
	})), slog.New(slog.DiscardHandler), time.Minute)
}

// getFrom serves h and requests it, accepting gzip.
func getFrom(t *testing.T, h http.Handler) (*http.Response, error) {
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	return http.DefaultClient.Do(req)
}

// TestCompressLeavesAPanicToPublic: a handler that panics before anything is flushed has sent the client nothing, so
// Public answers 500 for it; Compress must not commit the answer it holds on the way out. After a flush the answer has
// begun, and the connection is aborted instead.
func TestCompressLeavesAPanicToPublic(t *testing.T) {
	resp, err := getFrom(t, panicking(t, false))
	if err != nil {
		t.Fatalf("a panic before anything was sent: %v, want a 500", err)
	}
	if err := resp.Body.Close(); err != nil || resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("a panic before anything was sent: %d (%v), want 500", resp.StatusCode, err)
	}
	if resp, err := getFrom(t, panicking(t, true)); err == nil {
		_, err := io.ReadAll(resp.Body)
		if closeErr := resp.Body.Close(); err == nil && closeErr == nil {
			t.Error("a panic after the flush ended as a complete response")
		}
	}
}
