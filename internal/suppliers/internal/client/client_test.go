package client_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/client"
)

var specs = []adapter.BucketSpec{
	{Name: "global", Window: 180 * time.Second, Limit: 3},
	{Name: "critical", Window: 1200 * time.Second, Limit: 1},
}

// clock is a time the tests move.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newClock() *clock { return &clock{time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)} }

func blockedUntil(t *testing.T, err error) time.Time {
	t.Helper()
	var b adapter.Blocked
	if !errors.Is(err, adapter.ErrBucketsFull) || !errors.As(err, &b) {
		t.Fatalf("error %v, want a Blocked wrapping ErrBucketsFull", err)
	}
	return b.Until
}

// TestARequestTakesEveryBucketOrNone: a request counted in two buckets is refused when either has no allowance left,
// and a refused request takes nothing from the other, so a full critical bucket does not use up the global one.
func TestARequestTakesEveryBucketOrNone(t *testing.T) {
	t.Parallel()
	c := newClock()
	l := client.NewLimiter(specs, c.now)
	if err := l.Take([]string{"global", "critical"}); err != nil {
		t.Fatal(err)
	}
	for range 5 { // critical is full: these take nothing from global
		if until := blockedUntil(t, l.Take([]string{"global", "critical"})); !until.Equal(c.t.Add(1200 * time.Second)) {
			t.Errorf("reopens at %v, want after the critical window", until)
		}
	}
	for i := range 2 { // two of global's three requests remain
		if err := l.Take([]string{"global"}); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	blockedUntil(t, l.Take([]string{"global"}))
	c.advance(180 * time.Second) // global's window passes, critical's does not
	if err := l.Take([]string{"global"}); err != nil {
		t.Errorf("after the window: %v", err)
	}
	blockedUntil(t, l.Take([]string{"critical"}))
	c.advance(1020 * time.Second)
	if err := l.Take([]string{"critical"}); err != nil {
		t.Errorf("critical after its window: %v", err)
	}
}

func TestABucketNobodyDeclaredIsAnError(t *testing.T) {
	t.Parallel()
	l := client.NewLimiter(specs, newClock().now)
	if err := l.Take([]string{"typo"}); err == nil || errors.Is(err, adapter.ErrBucketsFull) {
		t.Errorf("an undeclared bucket: %v, want a plain error", err)
	}
}

func newClient(t *testing.T, server *httptest.Server) (*client.Client, *clock) {
	t.Helper()
	c := newClock()
	cl, err := client.New(server.URL, egress.NewForTesting(egresstest.Loopback(t)), client.NewLimiter(specs, c.now), c.now)
	if err != nil {
		t.Fatal(err)
	}
	return cl, c
}

// TestARefusalClosesTheBucketsTheRequestCounted: a 429 with an empty body closes every bucket the request named until
// its Retry-After and sends nothing more meanwhile; afterwards requests flow again.
func TestARefusalClosesTheBucketsTheRequestCounted(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	var got atomic.Value // what the second request looked like to the server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1200")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		got.Store(r.Method + " " + r.URL.RequestURI() + " " + r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cl, c := newClient(t, server)
	req := adapter.Request{Method: "GET", Path: "/api/x?p=1", Header: http.Header{"Authorization": {"Bearer t"}}, Buckets: []string{"global", "critical"}}
	_, err := cl.Do(t.Context(), req)
	if until := blockedUntil(t, err); !until.Equal(c.t.Add(1200 * time.Second)) {
		t.Errorf("blocked until %v, want Retry-After from now", until)
	}
	req.Buckets = []string{"global"} // also closed: the refusal did not say which bucket was exceeded
	if _, err := cl.Do(t.Context(), req); !errors.Is(err, adapter.ErrBucketsFull) {
		t.Errorf("a request in the closed global bucket: %v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("%d requests reached the server, want only the refused one", hits.Load())
	}
	c.advance(1200 * time.Second)
	if _, err := cl.Do(t.Context(), req); err != nil || got.Load() != "GET /api/x?p=1 Bearer t" {
		t.Errorf("after Retry-After: %v, the server saw %v", err, got.Load())
	}
}

// TestRequestsGoOnlyToTheOrigin: the path cannot change the host, and the origin must be a bare origin.
func TestRequestsGoOnlyToTheOrigin(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	cl, _ := newClient(t, server)
	for _, path := range []string{"//evil.example/x", "@evil.example/x", "http://evil.example/x", ""} {
		if _, err := cl.Do(t.Context(), adapter.Request{Method: "GET", Path: path, Buckets: []string{"global"}}); err == nil {
			t.Errorf("path %q was sent", path)
		}
	}
	for _, origin := range []string{"https://x.example/v1", "https://u:p@x.example", "ftp://x.example", "x.example", "https://x.example?a=1"} {
		if _, err := client.New(origin, nil, nil, nil); err == nil {
			t.Errorf("origin %q accepted", origin)
		}
	}
}

func TestAnOversizedResponseIsRefused(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(9<<20))
		if _, err := w.Write([]byte(strings.Repeat("x", 9<<20))); err != nil {
			t.Log(err) // the client may stop reading at its limit
		}
	}))
	defer server.Close()
	cl, _ := newClient(t, server)
	if _, err := cl.Do(t.Context(), adapter.Request{Method: "GET", Path: "/big", Buckets: []string{"global"}}); !errors.Is(err, client.ErrTooLarge) {
		t.Errorf("a 9 MiB response: %v", err)
	}
}

// TestARefusalCountsWhateverItsBodyDoes: the 429 and its Retry-After close the buckets even when the body is cut short or
// larger than the limit, which would otherwise be read as an error and leave the buckets open.
func TestARefusalCountsWhateverItsBodyDoes(t *testing.T) {
	t.Parallel()
	cases := map[string]func(w http.ResponseWriter){
		"cut short": func(w http.ResponseWriter) {
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(http.StatusTooManyRequests)
			if _, err := w.Write([]byte("x")); err != nil {
				return
			}
		},
		"too large": func(w http.ResponseWriter) {
			w.Header().Set("Content-Length", strconv.Itoa(9<<20))
			w.WriteHeader(http.StatusTooManyRequests)
			if _, err := w.Write([]byte(strings.Repeat("x", 9<<20))); err != nil {
				t.Log(err)
			}
		},
	}
	for name, respond := range cases {
		var hits atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.Header().Set("Retry-After", "600")
			respond(w)
		}))
		cl, c := newClient(t, server)
		req := adapter.Request{Method: "GET", Path: "/x", Buckets: []string{"global"}}
		if until := blockedUntil(t, doErr(t, cl, req)); !until.Equal(c.t.Add(600 * time.Second)) {
			t.Errorf("%s: blocked until %v", name, until)
		}
		blockedUntil(t, doErr(t, cl, req))
		if hits.Load() != 1 {
			t.Errorf("%s: %d requests reached the server, want only the refused one", name, hits.Load())
		}
		server.Close()
	}
}

func doErr(t *testing.T, cl *client.Client, r adapter.Request) error {
	t.Helper()
	_, err := cl.Do(t.Context(), r)
	return err
}
