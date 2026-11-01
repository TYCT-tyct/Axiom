package websearch_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/websearch"
)

//nolint:cyclop // the handler checks the request and the test checks the answer, one assertion each
func TestFirecrawlSearch(t *testing.T) {
	var got struct {
		Query          string   `json:"query"`
		Limit          int      `json:"limit"`
		IncludeDomains []string `json:"includeDomains"`
		ExcludeDomains []string `json:"excludeDomains"`
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/search" {
			t.Errorf("%s %s, want POST /v2/search", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fc-key" {
			t.Errorf("Authorization = %q, want the configured key", got)
		}
		if err := jsonx.Decode(r.Body, &got); err != nil {
			t.Errorf("the request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"success":true,"data":{"web":[
			{"url":"https://example.com/one","title":"One","description":"First"},
			{"url":"https://example.com/one","title":"again","description":"duplicate"},
			{"url":"ftp://example.com/two","title":"Two","description":"not http"},
			{"url":"https://example.org/three","title":"Three","description":"Third"}]}}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	client := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	searcher, err := websearch.OpenFirecrawl(websearch.Config{URL: server.URL + "/v2", Key: secret.New("fc-key")}, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	results, err := searcher.Search(ctx, websearch.Query{
		Text: "axiom", MaxResults: 5, IncludeDomains: []string{"example.com"},
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if got.Query != "axiom" || got.Limit != 5 || len(got.IncludeDomains) != 1 || got.IncludeDomains[0] != "example.com" || len(got.ExcludeDomains) != 0 {
		t.Errorf("request = %+v, want the query, limit and include domains", got)
	}
	if len(results) != 2 || results[0].URL != "https://example.com/one" || results[0].Content != "First" ||
		results[1].URL != "https://example.org/three" {
		t.Fatalf("results = %+v, want two valid, deduplicated results", results)
	}
}

func TestFirecrawlBothFiltersBecomeQueryOperators(t *testing.T) {
	var got struct {
		Query          string   `json:"query"`
		IncludeDomains []string `json:"includeDomains"`
		ExcludeDomains []string `json:"excludeDomains"`
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := jsonx.Decode(r.Body, &got); err != nil {
			t.Errorf("the request body is not JSON: %v", err)
		}
		if _, err := w.Write([]byte(`{"success":true,"data":{"web":[]}}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	client := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	searcher, err := websearch.OpenFirecrawl(websearch.Config{URL: server.URL, Key: secret.New("fc-key")}, client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := searcher.Search(ctx, websearch.Query{
		Text:           "axiom",
		IncludeDomains: []string{"example.com"}, ExcludeDomains: []string{"bad.example"},
	}); err != nil {
		t.Fatal(err)
	}
	if got.Query != "axiom site:example.com -site:bad.example" {
		t.Errorf("query = %q, want the domain operators", got.Query)
	}
	if len(got.IncludeDomains) != 0 || len(got.ExcludeDomains) != 0 {
		t.Errorf("both filter fields were sent: %+v", got)
	}
}

func TestFirecrawlRefusesAPreconditionAtTheBoundary(t *testing.T) {
	client := egress.NewForTesting(egresstest.Loopback(t))
	if _, err := websearch.OpenFirecrawl(websearch.Config{URL: "https://api.firecrawl.dev/v2"}, client); err == nil {
		t.Fatal("OpenFirecrawl() without a key succeeded")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	searcher, err := websearch.OpenFirecrawl(websearch.Config{URL: server.URL, Key: secret.New("fc-key")}, client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = searcher.Search(t.Context(), websearch.Query{Text: strings.Repeat("x", 501)})
	if !errors.Is(err, websearch.ErrQueryTooLong) {
		t.Fatalf("Search() error = %v, want ErrQueryTooLong", err)
	}
	server.Close()
}
