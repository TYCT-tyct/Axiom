package main

import (
	"context"
	"log/slog"
	"net/http"

	"axiom/internal/platform/consoleapi"
	"axiom/internal/platform/httpserver"
)

// consoleAPI implements the console's API (api/console.yaml).
type consoleAPI struct{ version string }

// GetVersion tells the console which build answers, uncached, so a page left open across a deployment notices it.
func (c consoleAPI) GetVersion(context.Context, consoleapi.GetVersionRequestObject) (consoleapi.GetVersionResponseObject, error) {
	noStore := "no-store"
	return consoleapi.GetVersion200JSONResponse{
		Body:    consoleapi.Version{Version: c.version},
		Headers: consoleapi.GetVersion200ResponseHeaders{CacheControl: &noStore},
	}, nil
}

// publicRoutes are the public listener's routes: so far the console's API. Every error path of the generated code
// is set here, none left at its default: a malformed request gets a 400 that describes only the request (the
// client's own input), and a handler's failure is logged and answered with a bare 500; the default would send the
// error's text, which may describe our internals, to the client.
func publicRoutes(api consoleapi.StrictServerInterface, logger *slog.Logger) http.Handler {
	badRequest := func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
	console := consoleapi.NewStrictHandlerWithOptions(api, nil, consoleapi.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: badRequest,
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error("console API handler failed", "err", err, "request_id", httpserver.RequestID(r.Context()))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		},
	})
	return consoleapi.HandlerWithOptions(console, consoleapi.StdHTTPServerOptions{
		BaseRouter:       http.NewServeMux(),
		ErrorHandlerFunc: badRequest,
	})
}
