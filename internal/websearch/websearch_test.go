package websearch_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
		{name: "credentials", env: map[string]string{"AXIOM_SEARCH_URL": "https://user:pass@search.example.com"}, wantErr: "must be an http(s) base URL"},
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

func TestOpenUnconfiguredIsOff(t *testing.T) {
	client := egress.NewForTesting(egresstest.Loopback(t))
	got, err := websearch.Open(websearch.Config{}, client)
	if err != nil || got != nil {
		t.Fatalf("Open(empty) = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestSearchParsesRecordedResponse(t *testing.T) {
	fixture, err := os.ReadFile("testdata/searxng.json")
	if err != nil {
		t.Fatal(err)
	}
	authorized := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorized = r.Header.Get("Authorization") == "Bearer search-key"
		if r.URL.Path != "/searxng/search" {
			t.Errorf("path = %q, want /searxng/search", r.URL.Path)
		}
		if got := r.URL.Query().Get("format"); got != "json" {
			t.Errorf("format = %q, want json", got)
		}
		if got, want := r.URL.Query().Get("q"), "axiom site:example.com -site:bad.example"; got != want {
			t.Errorf("q = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer server.Close()
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
	if !authorized {
		t.Fatal("the backend did not receive the configured bearer token")
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

func TestSearchBackendFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		headers map[string]string
	}{
		{name: "status", status: http.StatusInternalServerError, body: `{"results":[]}`},
		{name: "not json", status: http.StatusOK, body: `not json`},
		{name: "too large", status: http.StatusOK, body: strings.Repeat("x", (4<<20)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
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
			if err != nil && (strings.Contains(err.Error(), "axiom") || strings.Contains(err.Error(), tc.body)) {
				t.Fatalf("the error quotes the query or the backend body: %v", err)
			}
		})
	}
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
