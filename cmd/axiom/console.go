package main

import (
	"context"
	"log/slog"
	"net/http"

	"axiom/internal/accounts"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
)

// consoleAPI implements the console's API (api/console.yaml). It stands behind the guard, which has found the site,
// the session and its permission before a method runs, and kept the request to the contract's schemas.
type consoleAPI struct {
	version  string
	accounts *accounts.Service
	db       *pg.DB
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
	writeErrorBody(w, status, consoleapi.Error{Error: consoleapi.ErrorDetail{Code: code, Message: message}})
}

func writeErrorBody(w http.ResponseWriter, status int, body consoleapi.Error) {
	encoded, err := jsonx.MarshalCanonical(body)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(encoded) //nolint:errcheck // the client went away; nothing is left to tell it
}

// publicRoutes are the public listener's routes: so far the console's API. Every error path of the generated code
// is set here, none left at its default, and each answers with the contract's error body: a malformed request gets a
// 400 that describes only the request (the client's own input), a path or method the contract lacks a 404, a
// refusal the status and code its error maps to, and any other handler failure is logged and answered with a bare
// 500; the default would send the error's text, which may describe our internals, to the client. Every response is
// uncached. The guard runs in front of the generated routes: they read the query before any middleware of theirs
// would run, and its order of checks is the answer's.
func publicRoutes(api consoleapi.StrictServerInterface, g *guard, logger *slog.Logger) http.Handler {
	console := consoleapi.NewStrictHandlerWithOptions(api, nil, consoleapi.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  invalidRequest,
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) { respond(w, r, logger, err) },
	})
	mux := http.NewServeMux()
	routes := consoleapi.HandlerWithOptions(console, consoleapi.StdHTTPServerOptions{BaseRouter: mux, ErrorHandlerFunc: invalidRequest})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		_, pattern := mux.Handler(r)
		if pattern == "" {
			writeError(w, http.StatusNotFound, consoleapi.ErrorCodeNotFound, "no such resource")
			return
		}
		// The mux sets the pattern on the request it serves, which is a copy once the guard has given it a deadline;
		// httpserver.Public labels its metrics with the pattern of the request it handed over.
		r.Pattern = pattern
		g.serve(w, r, pattern, routes)
	})
}

// invalidRequest answers a request the generated code could not read. The guard has checked the request against the
// contract by then, so this is a corner the schemas do not reach; the error says what the parser saw, and a value
// the client sent can be in it, so it is not repeated.
func invalidRequest(w http.ResponseWriter, _ *http.Request, _ error) {
	writeError(w, http.StatusBadRequest, consoleapi.ErrorCodeInvalidRequest, "the request could not be read")
}
