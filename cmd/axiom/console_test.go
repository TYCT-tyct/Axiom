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
			routes := publicRoutes(failingConsole{}, slog.New(slog.NewJSONHandler(&logs, nil)))
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
