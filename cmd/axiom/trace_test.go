package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/httpserver"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/platform/telemetry"
)

// marker stands for an identifier or credential in a request's path, query or statement.
const marker = "planted-marker-value"

// workRoutes handles GET /work/{id} with a transaction and an outbound request, both on the request's context.
func workRoutes(t *testing.T, db *pg.DB, client *egress.Client, upstream string) http.Handler {
	t.Helper()
	routes := http.NewServeMux()
	routes.HandleFunc("GET /work/{id}", func(_ http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := db.Tx(ctx, pg.Scope{}, func(tx pg.Tx) error {
			_, err := tx.Exec(ctx, "SELECT '"+marker+"'")
			return err
		}); err != nil {
			t.Error(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream+"/v1/"+marker+"?key="+marker, nil)
		if err != nil {
			t.Error(err)
			return
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		if err := resp.Body.Close(); err != nil {
			t.Error(err)
		}
	})
	return routes
}

// tracedRequest sends one request through the public stack and returns the spans it made, the request ID the client
// was given and the upstream's host.
func tracedRequest(t *testing.T) (spans []sdktrace.ReadOnlySpan, requestID, upstreamHost string) {
	t.Helper()
	recorded := tracetest.NewSpanRecorder()
	tel, err := telemetry.New(recorded)
	if err != nil {
		t.Fatal(err)
	}
	db, err := pg.Open(t.Context(), pgtest.New(t, modules()...).App, pg.WithTracerProvider(tel.Traces))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := egress.NewForTesting(egresstest.Loopback(t), egress.WithTracerProvider(tel.Traces))
	requests, err := httpserver.NewTelemetry(tel.Traces, tel.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	routes := workRoutes(t, db, client, upstream.URL)
	front := httptest.NewServer(httpserver.Public(routes, slog.New(slog.DiscardHandler), time.Minute, httpserver.WithTelemetry(requests)))
	defer front.Close()
	resp, err := get(t, front.URL+"/work/"+marker)
	if err != nil {
		t.Fatal(err)
	}
	return recorded.Ended(), resp.Header.Get("X-Request-ID"), target.Hostname()
}

// TestRequestTraceCarriesTheRequestID: a public request is one trace. Its server span holds the request ID the
// client sees in X-Request-ID; the transaction and the outbound request it made are its children, and they keep the
// attributes they carry when sampled; and no span holds the path, the query or any statement, which may carry
// identifiers or content.
func TestRequestTraceCarriesTheRequestID(t *testing.T) {
	t.Parallel()
	spans, requestID, upstreamHost := tracedRequest(t)
	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range spans {
		byName[s.Name()] = s
		for _, a := range s.Attributes() {
			if strings.Contains(a.Value.String(), marker) {
				t.Errorf("span %s attribute %s holds the marker", s.Name(), a.Key)
			}
		}
	}
	server, tx, out := byName["GET /work/{id}"], byName["db.tx"], byName["egress.request"]
	if server == nil || tx == nil || out == nil {
		t.Fatalf("spans = %v, want the request, its transaction and its outbound request", names(spans))
	}
	if got := attribute(server, "request_id"); got != requestID || requestID == "" {
		t.Errorf("server span request_id = %q, the client saw %q", got, requestID)
	}
	if got := attribute(tx, "db.system"); got != "postgresql" {
		t.Errorf("transaction span db.system = %q, want postgresql", got)
	}
	if got := attribute(out, "server.address"); got != upstreamHost {
		t.Errorf("outbound span server.address = %q, want %q", got, upstreamHost)
	}
	if got := attribute(out, "http.response.status_code"); got != "200" {
		t.Errorf("outbound span status = %q, want 200", got)
	}
	for _, child := range []sdktrace.ReadOnlySpan{tx, out} {
		if child.Parent().SpanID() != server.SpanContext().SpanID() || child.SpanContext().TraceID() != server.SpanContext().TraceID() {
			t.Errorf("span %s is not a child of the request's span", child.Name())
		}
	}
}

func names(spans []sdktrace.ReadOnlySpan) []string {
	out := make([]string, len(spans))
	for i, s := range spans {
		out[i] = s.Name()
	}
	return out
}

func attribute(s sdktrace.ReadOnlySpan, key string) string {
	for _, a := range s.Attributes() {
		if string(a.Key) == key {
			return a.Value.String()
		}
	}
	return ""
}
