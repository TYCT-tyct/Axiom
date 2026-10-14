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

// publicRoutes are the public listener's routes: so far the console's API. A handler's error is logged and answered
// with a bare 500; the generated default would send the error's text to the client.
func publicRoutes(logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	console := consoleapi.NewStrictHandlerWithOptions(consoleAPI{version: version()}, nil, consoleapi.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, err.Error(), http.StatusBadRequest) // what was wrong with the client's own request
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error("console API handler failed", "err", err, "request_id", httpserver.RequestID(r.Context()))
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		},
	})
	consoleapi.HandlerFromMux(console, mux)
	return mux
}
