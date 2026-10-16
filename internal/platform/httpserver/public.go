package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
	"uuid"
)

// Public wraps the public listener's routes with what every request gets: a request ID of our own (a client's
// X-Request-Id is ignored, so it cannot forge or collide with ours), a bound on sending the request body, one
// access-log line, and a recovered panic. The log names the route pattern, never the path, which may hold
// identifiers or secrets.
//
// A request with a body must deliver it within bodyTimeout. Without that, a client that announces a body and never
// sends it holds the connection forever: the server waits to drain the body, and neither the header nor the idle
// timeout covers that wait. net/http clears the read deadline itself once the body ends, so a long streamed
// response is not cut by it (TestPublicLongRequestsOutliveTheBodyDeadline holds it to that); a request without a
// body has nothing to stall on and gets none.
func Public(routes http.Handler, logger *slog.Logger, bodyTimeout time.Duration, opts ...Option) http.Handler {
	o := apply(opts)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uuid.NewV7().String()
		w.Header().Set("X-Request-ID", id)
		ctx, finish := o.telemetry.start(context.WithValue(r.Context(), requestIDKey{}, id), id)
		r = r.WithContext(ctx)
		log := logger.With("request_id", id)
		if r.Body != http.NoBody {
			if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(bodyTimeout)); err != nil {
				log.Warn("cannot bound the request body", "err", err)
			}
		}
		recorder := &recorder{ResponseWriter: w}
		start := time.Now()
		defer func() {
			abort := false
			if v := recover(); v != nil {
				abort = recovered(recorder, v, log)
			}
			status := recorder.status
			if status == 0 && !abort {
				status = http.StatusOK // the handler wrote nothing; the server sends an empty 200
			}
			finish(r.Pattern, status, abort, time.Since(start))
			log.Info("request", "method", r.Method, "route", r.Pattern, "status", status,
				"bytes", recorder.bytes, "duration", time.Since(start))
			if abort {
				panic(http.ErrAbortHandler) // net/http closes the connection without logging it again
			}
		}()
		routes.ServeHTTP(recorder, r)
	})
}

type requestIDKey struct{}

// RequestID is the ID Public gave the request whose context ctx is, for handlers' own log lines.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// recovered answers a panic and reports whether the connection must be aborted: a 500 while nothing has been sent;
// once the status is out, aborting is the only way left to tell the client the response is broken, rather than
// letting it end as if complete. A handler's own http.ErrAbortHandler is a deliberate abort, not a bug.
func recovered(w *recorder, v any, logger *slog.Logger) (abort bool) {
	if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		return true
	}
	logger.Error("handler panicked", "panic", v, "stack", string(debug.Stack()))
	if w.status != 0 {
		return true
	}
	http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	return false
}

// recorder notes the status and size of a response for the access log and for panic handling. Its FlushError is
// what http.ResponseController calls, so a flush that sends the headers counts as the response having started;
// Unwrap lets the controller reach the connection underneath for deadlines.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 && status >= http.StatusOK { // 1xx responses are interim
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

func (r *recorder) FlushError() error {
	if r.status == 0 {
		r.status = http.StatusOK // flushing sends an implicit 200
	}
	return http.NewResponseController(r.ResponseWriter).Flush()
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
