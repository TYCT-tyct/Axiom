package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/webui"
)

// failingConsole is a console API whose handler fails with an error describing our internals.
type failingConsole struct{ consoleAPI }

func (failingConsole) GetVersion(context.Context, consoleapi.GetVersionRequestObject) (consoleapi.GetVersionResponseObject, error) {
	return nil, errors.New("planted-internal-detail")
}

// answer is what a client gets for the request "METHOD path" (GET when no method is given) from a console whose
// handler fails, and what the log recorded.
func answer(t *testing.T, request string) (rec *httptest.ResponseRecorder, logs string) {
	t.Helper()
	method, path := http.MethodGet, request
	if m, p, ok := strings.Cut(request, " "); ok {
		method, path = m, p
	}
	var log bytes.Buffer
	rec = httptest.NewRecorder()
	routes := publicRoutes(failingConsole{}, &guard{contract: loadedContract(t)}, testFrontend(t), slog.New(slog.NewJSONHandler(&log, nil)))
	routes.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
	return rec, log.String()
}

// TestPublicRoutesAnswerWithTheErrorBody: every failure of the generated code reaches the client as the contract's
// error body, and a handler's error reaches the log but not the client.
func TestPublicRoutesAnswerWithTheErrorBody(t *testing.T) {
	cases := []struct {
		name, request string
		status        int
		code          consoleapi.ErrorCode
	}{
		{"a handler fails", "/api/console/v1/version", http.StatusInternalServerError, consoleapi.ErrorCodeInternal},
		{"a path the contract lacks", "/api/console/v1/nothing-here", http.StatusNotFound, consoleapi.ErrorCodeNotFound},
		{"a method the contract lacks", "POST /api/console/v1/version", http.StatusNotFound, consoleapi.ErrorCodeNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, logs := answer(t, c.request)
			var body consoleapi.Error
			if err := jsonx.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("the client got %d %q, which is not the error body: %v", rec.Code, rec.Body, err)
			}
			if rec.Code != c.status || body.Error.Code != c.code {
				t.Errorf("the client got %d %q, want %d %q", rec.Code, body.Error.Code, c.status, c.code)
			}
			checkSecrecy(t, rec, logs, c.status == http.StatusInternalServerError)
		})
	}
}

// TestHandlerErrorThroughARealRequest: a request the guard admits fails in the handler on a real database error; the
// client gets the contract's bare 500, and the log has the query's error but the answer does not.
func TestHandlerErrorThroughARealRequest(t *testing.T) {
	f := newConsoleFixture(t)
	s := f.owner()
	f.run(`ALTER TABLE accounts.api_keys RENAME TO api_keys_missing`)
	rec := f.do(http.MethodGet, "/keys", "", as(s))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("the client got %d %q, want 500", rec.Code, rec.Body)
	}
	var body consoleapi.Error
	decode(t, rec, &body)
	want := consoleapi.Error{Error: consoleapi.ErrorDetail{Code: consoleapi.ErrorCodeInternal, Message: "internal error"}}
	if diff := cmp.Diff(want, body); diff != "" {
		t.Errorf("the client's error body (-want +got):\n%s", diff)
	}

	failure := failedHandler(t, f.logs.String())
	if !strings.Contains(failure.Err, "accounts.api_keys") || !strings.Contains(failure.Err, "does not exist") {
		t.Errorf("the log does not have the query's error: %q", failure.Err)
	}
	for _, detail := range []string{failure.Err, "accounts.api_keys", "does not exist"} {
		if strings.Contains(rec.Body.String(), detail) {
			t.Errorf("the answer repeats the database error %q: %s", detail, rec.Body)
		}
	}
}

// handlerFailure is the program's log record of a handler's failure.
type handlerFailure struct {
	Msg string `json:"msg"`
	Err string `json:"err"`
}

// failedHandler reads the record of the handler's failure from the log.
func failedHandler(t *testing.T, logs string) handlerFailure {
	t.Helper()
	for line := range strings.SplitSeq(logs, "\n") {
		if line == "" {
			continue
		}
		var record handlerFailure
		if err := jsonx.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log record %q: %v", line, err)
		}
		if record.Msg == "console API handler failed" {
			return record
		}
	}
	t.Fatalf("no handler-failure record in the log: %s", logs)
	return handlerFailure{}
}

// checkSecrecy: a response is uncached JSON that does not tell the client the handler's error, and the log has it
// exactly when the handler failed.
func checkSecrecy(t *testing.T, rec *httptest.ResponseRecorder, logs string, failed bool) {
	t.Helper()
	if rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", rec.Header())
	}
	if strings.Contains(rec.Body.String(), "planted-internal-detail") {
		t.Errorf("the client was told the handler's error: %s", rec.Body)
	}
	if strings.Contains(logs, "planted-internal-detail") != failed {
		t.Errorf("the handler's error logged = %v for a failed = %v: %s", !failed, failed, logs)
	}
}

// loadedContract is the console's contract as the program loads it.
func loadedContract(t testing.TB) *contract {
	t.Helper()
	c, err := consoleContract()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// testFrontend is a frontend of three one-line apps, so the routes are tested without the build.
func testFrontend(t *testing.T) *webui.Frontend {
	t.Helper()
	files := fstest.MapFS{"content-security-policy.txt": {Data: []byte("default-src 'self'")}}
	for _, app := range []string{"site", "console", "admin"} {
		files[app+"/index.html"] = &fstest.MapFile{Data: []byte("<" + app + ">")}
	}
	frontend, err := webui.New(files)
	if err != nil {
		t.Fatal(err)
	}
	return frontend
}

// TestPublicRoutesServePagesBesideTheAPI: the pages answer what the API does not claim, and an API path nothing claims
// stays a 404 for the caller, even to a browser, not the website's entry page with a 200.
func TestPublicRoutesServePagesBesideTheAPI(t *testing.T) {
	cases := []struct {
		name, target, accept string
		status               int
		body                 string
	}{
		{"the website", "/", "text/html", 200, "<site>"},
		{"the console", "/console/keys", "text/html", 200, "<console>"},
		{"the API", "/api/console/v1/version", "*/*", 200, ""},
		{"an API path no route claims", "/api/console/v1/nothing-here", "text/html", 404, ""},
		{"the model calls' prefix", "/v1/chat/completions", "text/html", 404, "Not Found\n"},
		{"a path of the admin, which the website's router does not know either", "/admin/", "text/html", 200, "<site>"},
	}
	routes := publicRoutes(consoleAPI{version: "v"}, &guard{contract: loadedContract(t)}, testFrontend(t), slog.New(slog.DiscardHandler))
	for _, c := range cases {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, c.target, nil)
		req.Header.Set("Accept", c.accept)
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		if rec.Code != c.status || (c.body != "" && rec.Body.String() != c.body) {
			t.Errorf("%s: %d %q, want %d %q", c.name, rec.Code, rec.Body, c.status, c.body)
		}
	}
}

// TestConsoleAnswersAreCompressed: a large answer of the console's API is gzipped for a client that accepts it, and a
// small one is not.
func TestConsoleAnswersAreCompressed(t *testing.T) {
	cases := []struct {
		version, accept string
		wantEncoding    string
	}{
		{strings.Repeat("v", 4000), "gzip", "gzip"},
		{strings.Repeat("v", 4000), "", ""},
		{"v1", "gzip", ""},
	}
	for _, c := range cases {
		routes := publicRoutes(consoleAPI{version: c.version}, &guard{contract: loadedContract(t)}, testFrontend(t), slog.New(slog.DiscardHandler))
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/console/v1/version", nil)
		req.Header.Set("Accept-Encoding", c.accept)
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		if got := rec.Header().Get("Content-Encoding"); got != c.wantEncoding || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%d bytes, Accept-Encoding %q: Content-Encoding %q, Cache-Control %q", len(c.version), c.accept, got, rec.Header().Get("Cache-Control"))
		}
	}
}

// TestPublicRoutesKeepTheAPIsMethodErrors: another method on an API path is the API's own 405 naming the one method the
// path takes, however a page would answer it, and a path no route has is a 404. Every answer outside the API, the
// routing's own 405 and the redirect included, carries the security headers.
func TestPublicRoutesKeepTheAPIsMethodErrors(t *testing.T) {
	cases := []struct {
		method, target, allow string
		status                int
		secured               bool
	}{
		{http.MethodGet, "/api/console/v1/auth/sign-in", "", 404, false},
		{http.MethodHead, "/api/console/v1/auth/sign-in", "", 404, false},
		{http.MethodPut, "/api/console/v1/auth/sign-in", "", 404, false},
		{http.MethodPut, "/api/console/v1/version", "", 404, false},
		{http.MethodGet, "/api/console/v1/nothing-here", "", 404, false},
		{http.MethodPost, "/", "GET, HEAD", 405, true},
		{http.MethodGet, "/console", "", 308, true},
		{http.MethodGet, "/", "", 200, true},
	}
	routes := publicRoutes(consoleAPI{version: "v"}, &guard{contract: loadedContract(t)}, testFrontend(t), slog.New(slog.DiscardHandler))
	for _, c := range cases {
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), c.method, c.target, nil))
		if rec.Code != c.status || rec.Header().Get("Allow") != c.allow {
			t.Errorf("%s %s: %d, Allow %q, want %d, %q", c.method, c.target, rec.Code, rec.Header().Get("Allow"), c.status, c.allow)
		}
		if got := rec.Header().Get("Content-Security-Policy"); c.secured && got != "default-src 'self'" {
			t.Errorf("%s %s: Content-Security-Policy %q", c.method, c.target, got)
		}
	}
}

// TestPublicRoutesSecureEveryAnswer: the answer the router itself gives for a path it cleans, a redirect, carries the
// security headers like any other, because they are set outside the router.
func TestPublicRoutesSecureEveryAnswer(t *testing.T) {
	routes := publicRoutes(consoleAPI{version: "v"}, &guard{contract: loadedContract(t)}, testFrontend(t), slog.New(slog.DiscardHandler))
	rec := httptest.NewRecorder()
	routes.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/console//keys", nil))
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "/console/keys" {
		t.Fatalf("GET /console//keys: %d to %q, want a redirect to /console/keys", rec.Code, rec.Header().Get("Location"))
	}
	for name, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "strict-origin-when-cross-origin",
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
	} {
		if got := rec.Header().Get(name); got != want {
			t.Errorf("the redirect's %s is %q, want %q", name, got, want)
		}
	}
}
