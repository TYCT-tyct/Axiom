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
)

// failingConsole is a console API whose handler fails with an error describing our internals.
type failingConsole struct{}

func (failingConsole) GetVersion(context.Context, consoleapi.GetVersionRequestObject) (consoleapi.GetVersionResponseObject, error) {
	return nil, errors.New("planted-internal-detail")
}

// TestPublicRoutesHideHandlerErrors: a handler's error reaches the log, and the client gets a bare 500 without it.
func TestPublicRoutesHideHandlerErrors(t *testing.T) {
	var logs bytes.Buffer
	rec := httptest.NewRecorder()
	routes := publicRoutes(failingConsole{}, slog.New(slog.NewJSONHandler(&logs, nil)))
	routes.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/console/v1/version", nil))
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != "Internal Server Error\n" {
		t.Errorf("the client got %d %q", rec.Code, rec.Body)
	}
	if !strings.Contains(logs.String(), "planted-internal-detail") {
		t.Errorf("the error was not logged: %s", &logs)
	}
}
