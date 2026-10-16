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

	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/webui"
)

// failingConsole is a console API whose handler fails with an error describing our internals.
type failingConsole struct{ pendingConsole }

func (failingConsole) GetVersion(context.Context, consoleapi.GetVersionRequestObject) (consoleapi.GetVersionResponseObject, error) {
	return nil, errors.New("planted-internal-detail")
}

// TestPublicRoutesAnswerWithTheErrorBody: every failure of the generated code reaches the client as the contract's
// error body, and a handler's error reaches the log but not the client.
func TestPublicRoutesAnswerWithTheErrorBody(t *testing.T) {
	cases := []struct {
		name, path string
		status     int
		code       consoleapi.ErrorCode
	}{
		{"a handler fails", "/api/console/v1/version", http.StatusInternalServerError, consoleapi.ErrorCodeInternal},
		{"an operation has no handler yet", "/api/console/v1/site", http.StatusNotImplemented, consoleapi.ErrorCodeNotImplemented},
		{"a parameter is malformed", "/api/console/v1/calls?limit=many", http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var logs bytes.Buffer
			rec := httptest.NewRecorder()
			routes := publicRoutes(failingConsole{}, testFrontend(t), slog.New(slog.NewJSONHandler(&logs, nil)))
			routes.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, c.path, nil))
			var body consoleapi.Error
			if err := jsonx.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("the client got %d %q, which is not the error body: %v", rec.Code, rec.Body, err)
			}
			if rec.Code != c.status || body.Error.Code != c.code || rec.Header().Get("Content-Type") != "application/json" {
				t.Errorf("the client got %d %q %q, want %d %q", rec.Code, rec.Header().Get("Content-Type"), body.Error.Code, c.status, c.code)
			}
			if strings.Contains(rec.Body.String(), "planted-internal-detail") {
				t.Errorf("the client was told the handler's error: %s", rec.Body)
			}
			if logged := strings.Contains(logs.String(), "planted-internal-detail"); logged != (c.status == http.StatusInternalServerError) {
				t.Errorf("the handler's error logged = %v for status %d: %s", logged, c.status, &logs)
			}
		})
	}
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
		{"an API path no route claims", "/api/console/v1/nothing-here", "text/html", 404, "404 page not found\n"},
		{"the model calls' prefix", "/v1/chat/completions", "text/html", 404, "Not Found\n"},
		{"a path of the admin, which the website's router does not know either", "/admin/", "text/html", 200, "<site>"},
	}
	routes := publicRoutes(consoleAPI{version: "v"}, testFrontend(t), slog.New(slog.DiscardHandler))
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
		routes := publicRoutes(consoleAPI{version: c.version}, testFrontend(t), slog.New(slog.DiscardHandler))
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
		{http.MethodGet, "/api/console/v1/auth/sign-in", "POST", 405, false},
		{http.MethodHead, "/api/console/v1/auth/sign-in", "POST", 405, false},
		{http.MethodPut, "/api/console/v1/auth/sign-in", "POST", 405, false},
		{http.MethodPut, "/api/console/v1/version", "GET, HEAD", 405, false},
		{http.MethodGet, "/api/console/v1/nothing-here", "", 404, false},
		{http.MethodPost, "/", "GET, HEAD", 405, true},
		{http.MethodGet, "/console", "", 308, true},
		{http.MethodGet, "/", "", 200, true},
	}
	routes := publicRoutes(consoleAPI{version: "v"}, testFrontend(t), slog.New(slog.DiscardHandler))
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
