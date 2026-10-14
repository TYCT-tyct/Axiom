package httpserver

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// public serves routes behind Public and returns the server and what it logged.
func public(t *testing.T, routes map[string]http.HandlerFunc) (*httptest.Server, *bytes.Buffer) {
	t.Helper()
	mux := http.NewServeMux()
	for pattern, handler := range routes {
		mux.HandleFunc(pattern, handler)
	}
	var logs bytes.Buffer
	node := httptest.NewServer(Public(mux, slog.New(slog.NewJSONHandler(&logs, nil))))
	t.Cleanup(node.Close)
	return node, &logs
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
func logged(t *testing.T, logs *bytes.Buffer) (records []map[string]any, counts map[string]int) {
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

// TestPublicRequestID: every response carries a fresh request ID, never the one a client sent.
func TestPublicRequestID(t *testing.T) {
	node, _ := public(t, map[string]http.HandlerFunc{"GET /": func(http.ResponseWriter, *http.Request) {}})
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

// TestPublicPanicMidResponse: once a response has started, a panic aborts the connection, so the client sees a
// broken response rather than one that ends as if complete; the request is still logged.
func TestPublicPanicMidResponse(t *testing.T) {
	node, logs := public(t, map[string]http.HandlerFunc{
		"GET /": func(w http.ResponseWriter, _ *http.Request) {
			write(t, w, "partial")
			if err := http.NewResponseController(w).Flush(); err != nil { // the wrapper must let streams flush
				t.Error(err)
			}
			panic("bug")
		},
	})
	if _, err := get(t, node.URL, nil); err == nil {
		t.Error("the response looked complete")
	}
	if _, counts := logged(t, logs); counts["handler panicked"] != 1 || counts["request"] != 1 {
		t.Errorf("logged %v", counts)
	}
}
