package sub2api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/jsonx"
	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// Recorded answers live in testdata/*.json, taken from sub2api 0.2.16 and scrubbed of every token, key, address and id:
// placeholders stand for them.

// raw keeps a JSON value as written.
type raw []byte

func (r *raw) UnmarshalJSON(b []byte) error { *r = append((*r)[:0], b...); return nil }

func fixture(t testing.TB, name string) adapter.Response {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json")) //nolint:gosec // a fixture of this package
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Status int               `json:"status"`
		Header map[string]string `json:"header"`
		Body   raw               `json:"body"`
	}
	if err := jsonx.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	for k, v := range doc.Header {
		h.Set(k, v)
	}
	return adapter.Response{Status: doc.Status, Header: h, Body: doc.Body}
}

// server is a fake supplier: a function from request to answer, and a record of what was asked.
type server struct {
	mu       sync.Mutex
	answer   func(adapter.Request) (adapter.Response, error)
	requests []adapter.Request
	declared map[string]bool
}

func newServer(answer func(adapter.Request) (adapter.Response, error)) *server {
	s := &server{answer: answer, declared: map[string]bool{}}
	for _, b := range New().Buckets() {
		s.declared[b.Name] = true
	}
	return s
}

// Do records the request, checks that it names only buckets the adapter declared and at least one, and answers.
func (s *server) Do(_ context.Context, r adapter.Request) (adapter.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, r)
	if len(r.Buckets) == 0 {
		return adapter.Response{}, fmt.Errorf("request %s %s names no rate bucket", r.Method, r.Path)
	}
	for _, b := range r.Buckets {
		if !s.declared[b] {
			return adapter.Response{}, fmt.Errorf("request %s %s names undeclared bucket %q", r.Method, r.Path, b)
		}
	}
	return s.answer(r)
}

func (s *server) paths() (out []string) {
	for _, r := range s.requests {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

// routes answers by "METHOD path" (the path with its query) from fixtures, and fails a request it has no answer for.
func routes(table map[string]adapter.Response) *server {
	return newServer(func(r adapter.Request) (adapter.Response, error) {
		if resp, ok := table[r.Method+" "+r.Path]; ok {
			return resp, nil
		}
		return adapter.Response{}, fmt.Errorf("no recorded answer for %s %s", r.Method, r.Path)
	})
}

// store is a LoginStore in memory with a version.
type store struct {
	mu      sync.Mutex
	raw     []byte
	version int64
	swaps   int
	stale   bool // the next Swap finds someone else's newer login
	other   []byte
}

func (s *store) Load(context.Context) ([]byte, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.raw), s.version, nil
}

func (s *store) Swap(_ context.Context, version int64, b []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stale {
		s.stale, s.raw, s.version = false, s.other, s.version+1
		return adapter.ErrStaleLogin
	}
	if version != s.version {
		return adapter.ErrStaleLogin
	}
	s.raw, s.version, s.swaps = slices.Clone(b), s.version+1, s.swaps+1
	return nil
}

var epoch = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// login is a stored login whose access token lives until expires.
func login(t testing.TB, expires time.Time) *store {
	t.Helper()
	l := Login{
		Email: "owner@example.test", Password: secret.New("PASSWORD_PLACEHOLDER"), Access: secret.New("ACCESS_OLD"),
		Refresh: secret.New("REFRESH_OLD"), AccessExpires: expires, Lifetime: 24 * time.Hour,
	}
	b, err := l.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return &store{raw: b, version: 1}
}

func (s *store) current(t testing.TB) Login {
	t.Helper()
	l, err := DecodeLogin(s.raw)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// clock is a time the test moves.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }
func (c *clock) set(t time.Time)         { c.t = t }

// managerAt opens a manager whose clock is clk.
func managerAt(t testing.TB, srv adapter.Client, st *store, clk *clock) adapter.Manager {
	t.Helper()
	m, err := NewWithClock(clk.now).Open(t.Context(), srv, st)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// scanPass reads one whole pass of the charge records as the module does: the first call, made with no position, only
// marks where the connection began; an hour later the next call reads what came after, page by page.
func scanPass(t *testing.T, m adapter.Manager, clk *clock) (all []adapter.Charge) {
	t.Helper()
	first, state, err := m.Charges(t.Context(), nil)
	if err != nil || !first.Done || len(first.Charges) != 0 || len(state) == 0 {
		t.Fatalf("the first scan: %+v, %q, %v; want an empty page, Done, and a position", first, state, err)
	}
	clk.advance(time.Hour)
	for calls := 0; ; calls++ {
		if calls > 8 {
			t.Fatal("the pass does not end")
		}
		page, next, err := m.Charges(t.Context(), state)
		if err != nil {
			t.Fatal(err)
		}
		all, state = append(all, page.Charges...), next
		if page.Done {
			return all
		}
	}
}

func manager0(t testing.TB, srv *server, st *store) adapter.Manager {
	t.Helper()
	m, err := NewWithClock(func() time.Time { return epoch }).Open(t.Context(), srv, st)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// readKeys reads the whole keys listing over as many reads as it takes, each from the position the one before gave, as
// the module does.
func readKeys(t testing.TB, m adapter.Manager) ([]adapter.KeyState, error) {
	t.Helper()
	var from adapter.Listing
	for range maxPages + 1 {
		page, err := m.Keys(t.Context(), from)
		if err != nil {
			return nil, err
		}
		if page.Done {
			return page.Items, nil
		}
		if len(page.Next) == 0 {
			t.Fatal("a read that did not end the listing gave no position to go on from")
		}
		from = page.Next
	}
	t.Fatal("the keys listing did not end")
	return nil, nil
}

// readOffer reads the whole offer the same way.
func readOffer(t testing.TB, m adapter.Manager) ([]adapter.Group, error) {
	t.Helper()
	var from adapter.Listing
	for range maxPages + 1 {
		page, err := m.Offer(t.Context(), from)
		if err != nil {
			return nil, err
		}
		if page.Done {
			return page.Items, nil
		}
		if len(page.Next) == 0 {
			t.Fatal("a read that did not end the offer gave no position to go on from")
		}
		from = page.Next
	}
	t.Fatal("the offer did not end")
	return nil, nil
}

// readPaged reads a listing over as many reads as its bucket allows: a read the bucket stopped is given again after the
// window reopens, from the position it answered with. It returns the read that ended the listing, and how often it had
// to wait.
func readPaged[T any](t *testing.T, clk *clock, read func(from adapter.Listing) (adapter.Page[T], error)) (adapter.Page[T], int) {
	t.Helper()
	var from adapter.Listing
	var page adapter.Page[T]
	waits := 0
	for range 500 {
		var err error
		if page, err = read(from); err != nil {
			t.Fatal(err)
		}
		if page.Done {
			return page, waits
		}
		if !page.Until.IsZero() {
			waits++
			clk.set(page.Until)
			continue
		}
		if len(page.Next) == 0 {
			t.Fatal("a read that did not end the listing gave no position to go on from")
		}
		from = page.Next
	}
	t.Fatal("the listing did not end")
	return page, waits
}
