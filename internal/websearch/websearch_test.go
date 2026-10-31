package websearch_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/config"
	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/secret"
	"axiom/internal/websearch"
)

func env(t *testing.T, values map[string]string) *config.Env {
	t.Helper()
	environ := make([]string, 0, len(values))
	for k, v := range values {
		environ = append(environ, k+"="+v)
	}
	return config.New(environ)
}

func TestReadConfig(t *testing.T) {
	// A base URL that carries a user is refused: the key belongs in AXIOM_SEARCH_KEY, not in the URL.
	withUser := (&url.URL{Scheme: "https", Host: "search.example.com", User: url.User("operator")}).String()
	for _, tc := range []struct {
		name    string
		env     map[string]string
		want    websearch.Config
		wantErr string
	}{
		{name: "off", env: nil},
		{name: "url only", env: map[string]string{"AXIOM_SEARCH_URL": "https://search.example.com/searxng/"}, want: websearch.Config{URL: "https://search.example.com/searxng"}},
		{name: "key only", env: map[string]string{"AXIOM_SEARCH_KEY": "secret"}, wantErr: "AXIOM_SEARCH_URL is not"},
		{name: "not a url", env: map[string]string{"AXIOM_SEARCH_URL": "search.example.com"}, wantErr: "must be an http(s) base URL"},
		{name: "credentials", env: map[string]string{"AXIOM_SEARCH_URL": withUser}, wantErr: "must be an http(s) base URL"},
		{name: "query", env: map[string]string{"AXIOM_SEARCH_URL": "https://search.example.com/?a=b"}, wantErr: "must be an http(s) base URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := websearch.ReadConfig(env(t, tc.env))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ReadConfig() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadConfig() error = %v", err)
			}
			if got.URL != tc.want.URL {
				t.Fatalf("ReadConfig().URL = %q, want %q", got.URL, tc.want.URL)
			}
		})
	}
}

// TestOpenRequiresAConfiguredBackend: the node checks Configured first, so a deployment with no backend never opens an
// adapter and keeps the documented refusal.
func TestOpenRequiresAConfiguredBackend(t *testing.T) {
	client := egress.NewForTesting(egresstest.Loopback(t))
	if _, err := websearch.Open(websearch.Config{}, client); err == nil {
		t.Fatal("Open() without a backend URL succeeded")
	}
}

func TestSearchParsesRecordedResponse(t *testing.T) {
	fixture, err := os.ReadFile("testdata/searxng.json")
	if err != nil {
		t.Fatal(err)
	}
	server := recordedBackend(t, fixture)
	client := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	searcher, err := websearch.Open(websearch.Config{URL: server.URL + "/searxng", Key: secret.New("search-key")}, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	results, err := searcher.Search(ctx, websearch.Query{
		Text: "axiom", MaxResults: 3,
		IncludeDomains: []string{"example.com"}, ExcludeDomains: []string{"bad.example"},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("Search() returned %d results, want 3: %+v", len(results), results)
	}
	want := []string{"https://example.com/one", "https://example.org/two", "https://example.net/three"}
	for i, r := range results {
		if r.URL != want[i] {
			t.Fatalf("result %d URL = %q, want %q", i, r.URL, want[i])
		}
	}
}

// recordedBackend serves the recorded SearXNG answer and checks the request the adapter sent for it.
func recordedBackend(t *testing.T, fixture []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/searxng/search" {
			t.Errorf("path = %q, want /searxng/search", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer search-key" {
			t.Errorf("Authorization = %q, want the configured bearer token", got)
		}
		if got := r.URL.Query().Get("format"); got != "json" {
			t.Errorf("format = %q, want json", got)
		}
		if got, want := r.URL.Query().Get("q"), "axiom site:example.com -site:bad.example"; got != want {
			t.Errorf("q = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(fixture); err != nil {
			t.Errorf("the backend could not write its body: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSearchBackendFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "status", status: http.StatusInternalServerError, body: `{"results":[]}`},
		{name: "not json", status: http.StatusOK, body: `not json`},
		{name: "too large", status: http.StatusOK, body: strings.Repeat("x", (4<<20)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := failingBackend(t, tc.status, tc.body)
			client := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
			searcher, err := websearch.Open(websearch.Config{URL: server.URL}, client)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			_, err = searcher.Search(ctx, websearch.Query{Text: "axiom"})
			if !errors.Is(err, websearch.ErrUpstream) {
				t.Fatalf("Search() error = %v, want ErrUpstream", err)
			}
			if strings.Contains(err.Error(), "axiom") || strings.Contains(err.Error(), tc.body) {
				t.Fatalf("the error quotes the query or the backend body: %v", err)
			}
		})
	}
}

// failingBackend answers every search with status and body.
func failingBackend(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("the backend could not write its body: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSearchRequiresText(t *testing.T) {
	client := egress.NewForTesting(egresstest.Loopback(t))
	searcher, err := websearch.Open(websearch.Config{URL: "https://search.example.com"}, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := searcher.Search(t.Context(), websearch.Query{Text: "  "}); !errors.Is(err, websearch.ErrNoQuery) {
		t.Fatalf("Search() error = %v, want ErrNoQuery", err)
	}
}

func TestSearchDoesNotLeakTheQuery(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	searcher, err := websearch.Open(websearch.Config{URL: server.URL}, egress.NewForTesting(egresstest.Loopback(t, server.Certificate())))
	if err != nil {
		t.Fatal(err)
	}
	server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err = searcher.Search(ctx, websearch.Query{Text: "client-secret-question"})
	if !errors.Is(err, websearch.ErrUpstream) {
		t.Fatalf("Search() error = %v, want ErrUpstream", err)
	}
	if strings.Contains(err.Error(), "client-secret-question") {
		t.Fatalf("the error leaks the query: %v", err)
	}
}
