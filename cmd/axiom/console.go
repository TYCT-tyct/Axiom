package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/jsonx"
)

//go:generate go run -C ../../tools ./pendingapi ../api/console.yaml ../cmd/axiom console_pending.gen.go axiom/internal/platform/consoleapi consoleAPI pendingConsole

// errNotImplemented is the answer of an operation of a contract that has no handler yet (tools/pendingapi).
var errNotImplemented = errors.New("not implemented")

// consoleAPI implements the console's API (api/console.yaml). An operation it declares no method for is answered by
// the embedded pendingConsole with a 501, until its handler is written.
type consoleAPI struct {
	pendingConsole
	version string
}

// GetVersion tells the console which build answers, uncached, so a page left open across a deployment notices it.
func (c consoleAPI) GetVersion(context.Context, consoleapi.GetVersionRequestObject) (consoleapi.GetVersionResponseObject, error) {
	noStore := "no-store"
	return consoleapi.GetVersion200JSONResponse{
		Body:    consoleapi.Version{Version: c.version},
		Headers: consoleapi.GetVersion200ResponseHeaders{CacheControl: &noStore},
	}, nil
}

// writeError answers with the contract's error body, which every failure of the API has.
func writeError(w http.ResponseWriter, status int, code consoleapi.ErrorCode, message string) {
	body, err := jsonx.MarshalCanonical(consoleapi.Error{Error: consoleapi.ErrorDetail{Code: code, Message: message}})
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body) //nolint:errcheck // the client went away; nothing is left to tell it
}

// publicRoutes are the public listener's routes: so far the console's API. Every error path of the generated code
// is set here, none left at its default, and each answers with the contract's error body: a malformed request gets a
// 400 that describes only the request (the client's own input), an operation without a handler a 501, and any other
// handler failure is logged and answered with a bare 500; the default would send the error's text, which may describe
// our internals, to the client.
func publicRoutes(api consoleapi.StrictServerInterface, logger *slog.Logger) http.Handler {
	badRequest := func(w http.ResponseWriter, _ *http.Request, err error) {
		writeError(w, http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest, err.Error())
	}
	console := consoleapi.NewStrictHandlerWithOptions(api, nil, consoleapi.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: badRequest,
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, errNotImplemented) {
				writeError(w, http.StatusNotImplemented, consoleapi.ErrorCodeNotImplemented, "this operation is not implemented yet")
				return
			}
			logger.Error("console API handler failed", "err", err, "request_id", httpserver.RequestID(r.Context()))
			writeError(w, http.StatusInternalServerError, consoleapi.ErrorCodeInternal, "internal error")
		},
	})
	return consoleapi.HandlerWithOptions(console, consoleapi.StdHTTPServerOptions{
		BaseRouter:       http.NewServeMux(),
		ErrorHandlerFunc: badRequest,
	})
}
