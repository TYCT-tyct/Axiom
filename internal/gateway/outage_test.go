package gateway

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/telemetry"
	"axiom/internal/protocol"
)

// refusals is the scraped count of requests refused for cause.
func refusals(t *testing.T, tel *telemetry.Telemetry, cause string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	tel.MetricsHandler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	for line := range strings.Lines(rec.Body.String()) {
		if value, ok := strings.CutPrefix(line, `axiom_gateway_outage_refusals_total{cause="`+cause+`"`); ok {
			return strings.TrimSpace(value[strings.LastIndex(value, " "):])
		}
	}
	return "absent from:\n" + rec.Body.String()
}

// TestAnOutageIsLoggedOnceNotPerRequest: however many requests an outage refuses, the log holds one line when it begins
// and one when it ends, while the metric counts every request, and a failure that is not an outage is still logged
// for each request.
func TestAnOutageIsLoggedOnceNotPerRequest(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, cause string
		err         error
	}{
		{"stale snapshot", causeSnapshot, fmt.Errorf("authenticate: %w", accounts.ErrStale)},
		{"database", causeDatabase, databaseDown(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tel, err := telemetry.New()
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			h := &Handler{Deps: Deps{Logger: logger}, outage: newOutage(logger, tel.Metrics.Meter("axiom/gateway"))}
			fail := func(err error) int {
				w := httptest.NewRecorder()
				h.fail(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", nil), protocol.Messages, err)
				return w.Code
			}
			const n = 500
			for range n {
				if code := fail(tc.err); code != http.StatusServiceUnavailable {
					t.Fatalf("an outage is answered %d, want 503", code)
				}
			}
			if lines := strings.Count(logs.String(), "\n"); lines != 1 {
				t.Errorf("%d requests refused by an outage wrote %d log lines, want 1:\n%s", n, lines, logs.String())
			}
			if got := refusals(t, tel, tc.cause); got != "500" {
				t.Errorf("the metric counts %s refusals, want %d", got, n)
			}
			h.outage.recovered()
			h.outage.recovered() // a request after the end tells nothing more
			ended := logs.String()[strings.Index(logs.String(), "\n")+1:]
			if strings.Count(ended, "\n") != 1 || !strings.Contains(ended, "refused=500") {
				t.Errorf("the end of the outage is not told once with its count:\n%s", ended)
			}
			logs.Reset()
			for range 3 {
				fail(errors.New("a bug"))
			}
			if lines := strings.Count(logs.String(), "\n"); lines != 3 {
				t.Errorf("3 failures that are not an outage wrote %d lines, want 3", lines)
			}
		})
	}
}

// databaseDown is the error a request meets when the database cannot be reached: the pool is lazy, so opening one for
// an address nothing listens on succeeds and the first transaction fails to connect.
func databaseDown(t *testing.T) error {
	t.Helper()
	db, err := pg.Open(t.Context(), "postgres://axiom:x@127.0.0.1:1/axiom?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	err = db.Tx(t.Context(), pg.Scope{}, func(pg.Tx) error { return nil })
	if _, ok := pg.Unavailable(err); !ok {
		t.Fatalf("an unreachable database gave %v", err)
	}
	return err
}
