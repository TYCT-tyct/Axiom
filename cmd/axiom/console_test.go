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

	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/jsonx"
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
	routes := publicRoutes(failingConsole{}, &guard{contract: loadedContract(t)}, slog.New(slog.NewJSONHandler(&log, nil)))
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
		{"a parameter is malformed", "/api/console/v1/calls?limit=many", http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest},
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
