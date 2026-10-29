// Package telemetrytest holds what tests need to read what the program writes: the logs its goroutines produce and the
// metrics text the ops listener serves (internal/platform/README.md「遥测」).
package telemetrytest

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"axiom/internal/platform/telemetry"
)

// Buffer collects what another goroutine writes while the test reads it; the lock is what keeps the race detector quiet
// about the handler's goroutines and the assertions.
type Buffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// Logger writes to the buffer in the text format the tests assert on.
func (b *Buffer) Logger() *slog.Logger { return slog.New(slog.NewTextHandler(b, nil)) }

// Text is what the ops listener's /metrics handler serves for the process's own providers.
func Text(t testing.TB, tel *telemetry.Telemetry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	tel.MetricsHandler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

// Metric is the value of series in Text, or Text itself when it is absent. series is what an operator's scrape shows
// before the value: a name, or a name and its labels, such as axiom_gateway_outage_refusals_total{dependency="database"}.
func Metric(t testing.TB, tel *telemetry.Telemetry, series string) string {
	t.Helper()
	body := Text(t, tel)
	for line := range strings.Lines(body) {
		if value, ok := strings.CutPrefix(line, series+" "); ok {
			return strings.TrimSpace(value)
		}
	}
	return "absent from:\n" + body
}
