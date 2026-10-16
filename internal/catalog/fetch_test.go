package catalog

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
)

// reply writes the body; a failed write is the test's failure, which t reports safely from the server's goroutine.
func reply(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write the reply: %v", err)
	}
}

func fetcherFor(t *testing.T, h http.HandlerFunc) *Fetcher {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	f := NewFetcher(egress.NewForTesting(egresstest.Loopback(t)))
	f.urls = map[PriceSource]string{SourceLiteLLM: srv.URL + "/models.json"}
	f.now = func() time.Time { return fetchedAt }
	return f
}

var fetchedAt = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func TestFetch(t *testing.T) {
	t.Parallel()
	f := fetcherFor(t, func(w http.ResponseWriter, _ *http.Request) { reply(t, w, `{"a":1}`) })
	got, err := f.Fetch(t.Context(), SourceLiteLLM)
	if err != nil || string(got.Body) != `{"a":1}` || got.Source != SourceLiteLLM || !got.At.Equal(fetchedAt) {
		t.Fatalf("Fetch = %+v, %v", got, err)
	}
	if _, err := f.Fetch(t.Context(), SourceOfficial); err == nil {
		t.Error("fetched a source that is not a dataset")
	}
	if _, err := f.Fetch(t.Context(), SourceOpenRouter); err == nil {
		t.Error("fetched an address that is not registered")
	}
}

// TestFetchRefuses: only a 200 is a dataset (a redirect is not followed, and an error page is not parsed), a body past the
// limit is refused, and a server that does not answer is cut off at the deadline.
func TestFetchRefuses(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		handler func(t *testing.T, w http.ResponseWriter, r *http.Request)
		limit   int64
		want    string
	}{
		"redirect": {func(_ *testing.T, w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		}, 1 << 10, "status 302"},
		"error": {func(_ *testing.T, w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "down", http.StatusBadGateway)
		}, 1 << 10, "status 502"},
		"too big": {func(t *testing.T, w http.ResponseWriter, _ *http.Request) { reply(t, w, strings.Repeat("x", 100)) }, 99, "exceeds limit"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := fetcherFor(t, func(w http.ResponseWriter, r *http.Request) { tc.handler(t, w, r) })
			f.maxBytes = tc.limit
			if _, err := f.Fetch(t.Context(), SourceLiteLLM); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Fetch: %v, want %q", err, tc.want)
			}
		})
	}
}
