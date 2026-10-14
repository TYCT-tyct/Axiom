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
func New(addr string, handler http.Handler, logger *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
}

// Health answers the probes: /livez while the process runs, /readyz only while it takes new work. A node reports
// not ready from startup until everything it needs is up, and again from the moment shutdown begins.
type Health struct{ ready atomic.Bool }

// SetReady records whether the node takes new work.
func (h *Health) SetReady(ready bool) { h.ready.Store(ready) }

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
