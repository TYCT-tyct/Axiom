package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
)

func TestPageExtractionIgnoresLayoutAndRejectsAmbiguity(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"table", `<nav>ignore</nav><h2>Start</h2><table><tr><td>alpha</td><td>$1 &amp; $2</td></tr></table><p>End</p>`, "alpha $1 & $2"},
		{"inline", `<h2>Start</h2><div>alpha <b>$1</b> &amp; $2</div><script>price=3</script><p hidden>noise</p>End`, "alpha $1 & $2"},
		{"missing", "not a price section", ""},
		{"ambiguous start", "Start Start alpha $1 End", ""},
		{"ambiguous end", "Start alpha $1 End End", ""},
		{"empty", "Start End", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractPriceSection([]byte(tc.body), "Start", "End")
			if tc.want == "" {
				if err == nil {
					t.Fatalf("accepted %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for _, pair := range [][2]string{{"$1 alpha", "$2 alpha"}, {"a b c", "a d c"}, {"甲乙", "甲丙"}, {"", "initial"}, {"same", "same"}} {
		removed, added := pageDifference(pair[0], pair[1])
		if strings.Replace(pair[0], removed, added, 1) != pair[1] {
			t.Fatalf("diff %q -> %q: %q/%q", pair[0], pair[1], removed, added)
		}
	}
}

func TestPageFetchRespectsRobotsLimitsAndRedirects(t *testing.T) {
	for _, tc := range []pageFetchCase{
		{name: "allow and crawl delay", robots: "User-agent: *\nDisallow: /private\nUser-agent: Axiom\nDisallow: /\nAllow: /price\nCrawl-delay: 12", wantPage: true},
		{name: "maximum crawl delay", robots: "User-agent: Axiom\nCrawl-delay: 60", wantPage: true},
		{name: "crawl delay exceeds budget", robots: "User-agent: Axiom\nCrawl-delay: 300"},
		{name: "deny", robots: "User-agent: *\nDisallow: /price"},
		{name: "wildcard", robots: "User-agent: Axiom\nDisallow: /*ice$"},
		{name: "specific group", robots: "User-agent: *\nDisallow: /\nUser-agent: Axiom\nAllow: /", wantPage: true},
		{name: "missing robots", robotsStatus: 404, wantPage: true},
		{name: "gone robots", robotsStatus: 410, wantPage: true},
		{name: "forbidden robots", robotsStatus: 403},
		{name: "unavailable robots", robotsStatus: 503},
		{name: "redirect robots", robotsStatus: 302},
		{name: "redirect page", pageStatus: 302, wantPage: true},
		{name: "rate limited", pageStatus: 429, retry: "7200", wantPage: true},
		{name: "large body", page: strings.Repeat("x", maxDatasetBytes+1), wantPage: true},
		{name: "missing marker", page: "layout changed", wantPage: true},
	} {
		t.Run(tc.name, tc.run)
	}
}

type pageFetchCase struct {
	name, robots, page, retry string
	robotsStatus, pageStatus  int
	wantPage                  bool
}

func (tc pageFetchCase) serve(t *testing.T, requests *atomic.Int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.UserAgent() != "Axiom" {
			t.Errorf("request %s / %s", r.Method, r.UserAgent())
		}
		body, status := tc.page, tc.pageStatus
		if body == "" {
			body = "Start alpha $1 End"
		}
		switch r.URL.Path {
		case "/robots.txt":
			body, status = tc.robots, tc.robotsStatus
		case "/price":
			requests.Add(1)
		default:
			t.Errorf("followed redirect to %s", r.URL.Path)
		}
		w.Header().Set("Location", "/redirect-target")
		w.Header().Set("Retry-After", tc.retry)
		if status != 0 {
			w.WriteHeader(status)
		}
		if _, err := fmt.Fprint(w, body); err != nil {
			t.Logf("test server: client closed response: %v", err)
		}
	}
}

func (tc pageFetchCase) run(t *testing.T) {
	t.Parallel()
	var requests atomic.Int64
	server := httptest.NewTLSServer(tc.serve(t, &requests))
	defer server.Close()
	client := egress.NewForTesting(egresstest.Loopback(t, server.Certificate()))
	defer client.CloseIdleConnections()
	var delays []time.Duration
	reserve := func(_ context.Context, _ string, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	}
	var backoff time.Duration
	pause := func(_ context.Context, _ string, delay time.Duration) error { backoff = delay; return nil }
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	got, err := fetchPage(ctx, client, PageTarget{Author: "acme", URL: server.URL + "/price", Currency: "USD", SectionStart: "Start", SectionEnd: "End"}, reserve, pause)
	good := tc.wantPage && tc.pageStatus == 0 && tc.page == ""
	if good {
		if err != nil || got.Text != "alpha $1" {
			t.Fatalf("content %q, error %v", got.Text, err)
		}
	} else if err == nil {
		t.Fatal("expected failed inspection")
	}
	if (requests.Load() == 1) != tc.wantPage {
		t.Fatalf("price page requests: %d", requests.Load())
	}
	tc.checkTiming(t, delays, backoff)
}

func (tc pageFetchCase) checkTiming(t *testing.T, delays []time.Duration, backoff time.Duration) {
	t.Helper()
	if tc.name == "allow and crawl delay" && delays[1] != 12*time.Second {
		t.Fatalf("delays: %v", delays)
	}
	if tc.pageStatus == 429 && backoff != 2*time.Hour {
		t.Fatalf("Retry-After: %s", backoff)
	}
}

func TestLiveInspectorSpacesSitesAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := NewPageInspector(nil)
		start := time.Now()
		for _, origin := range []string{"https://a.test", "https://b.test", "https://a.test"} {
			if err := p.reserve(t.Context(), origin, 10*time.Second); err != nil {
				t.Fatal(err)
			}
		}
		if time.Since(start) != 10*time.Second {
			t.Fatal("origins did not share their own rate")
		}
		if err := p.backoff(t.Context(), "https://a.test", time.Hour); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := p.reserve(ctx, "https://a.test", time.Second); err == nil {
			t.Fatal("cancelled reservation proceeded")
		}
		start = time.Now()
		if err := p.reserve(t.Context(), "https://a.test", time.Second); err != nil {
			t.Fatal(err)
		}
		if time.Since(start) != time.Hour {
			t.Fatal("Retry-After was ignored")
		}
	})
}

func TestWatchedPageBoundaryValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*PageTarget)
	}{
		{"HTTP", func(p *PageTarget) { p.URL = "http://example.com/prices" }},
		{"credentials", func(p *PageTarget) { p.URL = "https://user:pass@example.com/prices" }},
		{"fragment", func(p *PageTarget) { p.URL = "https://example.com/prices#table" }},
		{"interval too short", func(p *PageTarget) { p.IntervalSeconds = 3599 }},
		{"rate exceeds budget", func(p *PageTarget) { p.RateSeconds = 300 }},
		{"rate above maximum", func(p *PageTarget) { p.RateSeconds = 61 }},
		{"rate zero override", func(p *PageTarget) { p.RateSeconds = -1 }},
		{"lowercase currency", func(p *PageTarget) { p.Currency = "usd" }},
		{"empty section", func(p *PageTarget) { p.SectionStart = " " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := PageTarget{Author: "acme", URL: "https://example.com/prices", Currency: "USD", SectionStart: "Start", SectionEnd: "End"}
			tc.edit(&p)
			if _, err := p.validate(); err == nil {
				t.Fatal("invalid target accepted")
			}
		})
	}
	p := PageTarget{Author: "acme", URL: "https://EXAMPLE.com:443/prices", Currency: "USD", SectionStart: "Start", SectionEnd: "End", RateSeconds: 60}
	origin, err := p.validate()
	if err != nil || origin != "https://example.com" || p.URL != "https://example.com/prices" {
		t.Fatalf("canonical site: %q, %q, %v", origin, p.URL, err)
	}
}

func TestLiveInspectorDoesNotRetainPreviousCrawlDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := NewPageInspector(nil)
		for i, delay := range []time.Duration{12 * time.Second, time.Second, time.Second} {
			start := time.Now()
			if err := p.reserve(t.Context(), "https://example.com", delay); err != nil {
				t.Fatal(err)
			}
			want := []time.Duration{0, 12 * time.Second, time.Second}[i]
			if got := time.Since(start); got != want {
				t.Fatalf("request %d: waited %s, want %s", i, got, want)
			}
		}
	})
}
