package httpserver

import (
	"bytes"
	"encoding/json/v2" //nolint:depguard // decodes the access log this package writes; jsonx guards input from outside
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// bodyTimeout is short in tests, so a stalled body is cut off quickly.
const bodyTimeout = 300 * time.Millisecond

// logs is the sink the server's goroutines write to while the test reads it. The race detector does not see the
// ordering a closed connection gives, so the lock makes it explicit.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// public serves routes behind Public and returns the server and what it logged.
func public(t *testing.T, routes map[string]http.HandlerFunc) (*httptest.Server, *logs) {
	t.Helper()
	mux := http.NewServeMux()
	for pattern, handler := range routes {
		mux.HandleFunc(pattern, handler)
	}
	sink := new(logs)
	node := httptest.NewServer(Public(mux, slog.New(slog.NewJSONHandler(sink, nil)), bodyTimeout))
	t.Cleanup(node.Close)
	return node, sink
}

// reply is what a test needs of a response it read in full.
type reply struct {
	StatusCode int
	Header     http.Header
}

// get requests url and reads the whole response; err covers a response that broke off.
func get(t *testing.T, url string, header http.Header) (reply, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	maps.Copy(req.Header, header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return reply{}, err
	}
	_, err = io.ReadAll(resp.Body)
	if closeErr := resp.Body.Close(); err == nil {
		err = closeErr
	}
	return reply{resp.StatusCode, resp.Header}, err
}

// logged decodes the JSON log lines and counts them by message.
func logged(t *testing.T, logs *logs) (records []map[string]any, counts map[string]int) {
	t.Helper()
	counts = map[string]int{}
	for line := range strings.Lines(logs.String()) {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		records = append(records, record)
		counts[record["msg"].(string)]++
	}
	return records, counts
}

func write(t *testing.T, w http.ResponseWriter, text string) {
	t.Helper()
	if _, err := io.WriteString(w, text); err != nil {
		t.Error(err)
	}
}

// TestPublicRequestID: every response carries a fresh request ID, never the one a client sent; a handler that
// writes nothing is logged with the 200 the server sends.
func TestPublicRequestID(t *testing.T) {
	node, logs := public(t, map[string]http.HandlerFunc{"GET /": func(http.ResponseWriter, *http.Request) {}})
	forged := http.Header{}
	forged.Set("X-Request-ID", "forged")
	ids := map[string]bool{}
	for range 2 {
		resp, err := get(t, node.URL, forged)
		if err != nil {
			t.Fatal(err)
		}
		ids[resp.Header.Get("X-Request-ID")] = true
	}
	if len(ids) != 2 || ids["forged"] || ids[""] {
		t.Errorf("request IDs %v: want two fresh ones", ids)
	}
	if records, _ := logged(t, logs); len(records) != 2 || records[0]["status"] != 200.0 {
		t.Errorf("access log %v, want status 200", records)
	}
}

// TestPublicAccessLog: one line per request, naming the route pattern, not the path, which may hold identifiers.
func TestPublicAccessLog(t *testing.T) {
	node, logs := public(t, map[string]http.HandlerFunc{
		"GET /keys/{id}": func(w http.ResponseWriter, _ *http.Request) { write(t, w, "hello") },
	})
	resp, err := get(t, node.URL+"/keys/key-secret-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	records, _ := logged(t, logs)
	want := map[string]any{"route": "GET /keys/{id}", "status": 200.0, "bytes": 5.0, "request_id": resp.Header.Get("X-Request-ID")}
	if len(records) != 1 || !containsAll(records[0], want) {
		t.Errorf("access log %v, want it to include %v", records, want)
	}
	if strings.Contains(logs.String(), "key-secret-1") {
		t.Error("the access log holds the request path")
	}
}

func containsAll(record, want map[string]any) bool {
	for key, value := range want {
		if record[key] != value {
			return false
		}
	}
	return true
}

// TestPublicPanicBeforeResponse: a panic before anything is sent becomes a 500 that carries the request ID.
func TestPublicPanicBeforeResponse(t *testing.T) {
	node, logs := public(t, map[string]http.HandlerFunc{"GET /": func(http.ResponseWriter, *http.Request) { panic("bug") }})
	resp, err := get(t, node.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusInternalServerError || resp.Header.Get("X-Request-ID") == "" {
		t.Errorf("%d with request ID %q, want 500 with one", resp.StatusCode, resp.Header.Get("X-Request-ID"))
	}
	if _, counts := logged(t, logs); counts["handler panicked"] != 1 || counts["request"] != 1 {
		t.Errorf("logged %v", counts)
	}
}

// TestPublicPanicMidResponse: once a response has started, by writing or by only flushing the headers, a panic
// aborts the connection, so the client sees a broken response rather than one that ends as if complete; the
// request is still logged.
func TestPublicPanicMidResponse(t *testing.T) {
	flush := func(w http.ResponseWriter) {
		if err := http.NewResponseController(w).Flush(); err != nil { // the wrapper must let streams flush
			t.Error(err)
		}
	}
	node, logs := public(t, map[string]http.HandlerFunc{
		"GET /written": func(w http.ResponseWriter, _ *http.Request) {
			write(t, w, "partial")
			flush(w)
			panic("bug")
		},
		"GET /flushed": func(w http.ResponseWriter, _ *http.Request) {
			flush(w)
			panic("bug")
		},
	})
	for _, path := range []string{"/written", "/flushed"} {
		if _, err := get(t, node.URL+path, nil); err == nil {
			t.Errorf("%s: the response looked complete", path)
		}
	}
	if _, counts := logged(t, logs); counts["handler panicked"] != 2 || counts["request"] != 2 {
		t.Errorf("logged %v", counts)
	}
}
