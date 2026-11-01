package main

import (
	"context"
	"log/slog"
	"net/http"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/payments"
	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/webui"
	"axiom/internal/websearch"
)

// consoleAPI implements the console's API (api/console.yaml). It stands behind the guard, which has found the site,
// the session and its permission before a method runs, and kept the request to the contract's schemas.
type consoleAPI struct {
	charges  *billing.ChargeReader
	exit     *egress.Client
	version  string
	accounts *accounts.Service
	payments *payments.Payments
	catalog  *catalog.Catalog
	db       *pg.DB
	// models is the public projection built with the last routing snapshot, so the page and a call charge one price.
	models modelProjection
	// search is the node's enabled engines: the model pages show what one search through each costs.
	search []websearch.Engine
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
func writeError(w http.ResponseWriter, status int, code errorCode, message string) {
	writeErrorBody(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

func writeErrorBody(w http.ResponseWriter, status int, body errorBody) {
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

// mounting is what the generated code of a contract needs to be mounted on the guarded router (apiRoutes): the router
// and the answer to a handler that failed.
type mounting struct {
	mux           *http.ServeMux
	handlerFailed func(http.ResponseWriter, *http.Request, error)
}

// apiRoutes are the routes of one contract, which mount registers on the router it is given and returns the handler of.
// Every error path of the generated code is set here, none left at its default, and each answers with the contract's
// error body: a malformed request gets a 400 that describes only the request (the client's own input), a path or method
// the contract lacks a 404, a refusal the status and code its error maps to, and any other handler failure is logged
// and answered with a bare 500; the default would send the error's text, which may describe our internals, to the
// client. Every response is uncached. The guard runs in front of the generated routes: they read the query before any
// middleware of theirs would run, and its order of checks is the answer's.
func apiRoutes(g *guard, logger *slog.Logger, mount func(mounting) http.Handler) http.Handler {
	mux := http.NewServeMux()
	routes := mount(mounting{
		mux:           mux,
		handlerFailed: func(w http.ResponseWriter, r *http.Request, err error) { respond(w, r, logger, err) },
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		_, pattern := mux.Handler(r)
		if pattern == "" {
			writeError(w, http.StatusNotFound, codeNotFound, "no such resource")
			return
		}
		// The mux sets the pattern on the request it serves, which is a copy once the guard has given it a deadline;
		// httpserver.Public labels its metrics with the pattern of the request it handed over.
		r.Pattern = pattern
		g.serve(w, r, pattern, routes)
	})
}

// servedRoutes mounts one contract's API under /api/ and the pages beside it at /, compressed and under the security
// headers: the public and the admin listener differ only in which of each they serve.
func servedRoutes(api http.Handler, secure func(http.Handler) http.Handler, pages http.Handler) http.Handler {
	// The API owns its prefix for every method, so what the contract does not route is the API's own 404, never a
	// page; the pages get the rest.
	routes := http.NewServeMux()
	routes.Handle("/api/", httpserver.Compress(api))
	routes.Handle("/", pages)
	return secure(routes) // outermost: the redirect for an unclean path is the router's own answer
}

// publicRoutes are the public listener's routes: the console's API, with its large JSON answers gzipped, and the
// website and console pages for every path the API does not claim.
func publicRoutes(api consoleapi.StrictServerInterface, g *guard, frontend *webui.Frontend, logger *slog.Logger) http.Handler {
	apiHandler := apiRoutes(g, logger, func(m mounting) http.Handler {
		console := consoleapi.NewStrictHandlerWithOptions(api, nil, consoleapi.StrictHTTPServerOptions{
			RequestErrorHandlerFunc: invalidRequest, ResponseErrorHandlerFunc: m.handlerFailed,
		})
		return consoleapi.HandlerWithOptions(console, consoleapi.StdHTTPServerOptions{BaseRouter: m.mux, ErrorHandlerFunc: invalidRequest})
	})
	return servedRoutes(apiHandler, frontend.Secured, frontend.Public())
}

// withModelCalls puts the model API in front of the public listener's routes: /v1 and /api/v1, the same calls under
// OpenRouter's prefix, for every method the gateway answers. They are mounted outside Compress because the gateway
// writes its answers itself, streams and keep-alives included, and decides when they are flushed, and outside the
// console's prefix so that no API's 404 answers a call. Only the public listener gets them: the admin one is behind a guard no model client has a session for.
func withModelCalls(routes http.Handler, calls http.Handler) http.Handler {
	mount := http.NewServeMux()
	mount.Handle("/v1/", calls)
	mount.Handle("/api/v1/", calls)
	mount.Handle("/", routes)
	return mount
}

// invalidRequest answers a request the generated code could not read. The guard has checked the request against the
// contract by then, so this is a corner the schemas do not reach; the error says what the parser saw, and a value
// the client sent can be in it, so it is not repeated.
func invalidRequest(w http.ResponseWriter, _ *http.Request, _ error) {
	writeError(w, http.StatusBadRequest, codeInvalidRequest, "the request could not be read")
}
