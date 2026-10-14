// Package httpserver holds what every Axiom listener shares: the server's limits and the health endpoints.
package httpserver

import (
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

// New returns a server for handler with the limits every listener shares. The header timeout stops connections that
// never finish their headers from piling up, and the idle timeout closes keep-alive connections a client abandoned;
// without it they would stay open forever. There is no overall write timeout: a streamed response runs as long as
// the model does, so each handler sets its own deadline.
func New(handler http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
}

// Health answers the probes: /livez while the process runs, /readyz only while it takes new work. A node reports
// not ready until everything it needs is up, and turns not ready again when shutdown begins, just before its
// listeners close.
type Health struct{ ready atomic.Bool }

// SetReady records whether the node takes new work.
func (h *Health) SetReady(ready bool) { h.ready.Store(ready) }

// probeTimeout bounds a whole probe exchange, request body included. Probes never stream, and without it a client
// that announces a body and never sends it holds the connection forever: the server waits to drain the body, and
// neither the header nor the idle timeout covers that wait.
const probeTimeout = 2 * time.Second

// Server returns the ops listener's server, which serves only the probes.
func (h *Health) Server(logger *slog.Logger) *http.Server {
	server := New(h.Handler(), logger)
	server.ReadTimeout, server.WriteTimeout = probeTimeout, probeTimeout
	return server
}

// Handler serves /livez and /readyz, for GET and HEAD.
func (h *Health) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !h.ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	return mux
}
