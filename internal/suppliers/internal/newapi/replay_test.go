package newapi

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"axiom/internal/platform/secret"
	"axiom/internal/suppliers/internal/adapter"
)

// sample reads a recorded exchange from testdata/, as written by the recorder of the research (scrubbed: secrets are
// placeholders): a "METHOD path" line, request lines starting with ">", and the response's status, headers and body
// on lines starting with "<".
func sample(t testing.TB, name string) adapter.Response {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name+".txt")) //nolint:gosec // a fixed directory and names the tests give
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read only
	resp := adapter.Response{Header: http.Header{}}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "< status: "):
			status, err := strconv.Atoi(strings.Fields(strings.TrimPrefix(line, "< status: "))[0])
			if err != nil {
				t.Fatal(err)
			}
			resp.Status = status
		case strings.HasPrefix(line, "< body: "):
			resp.Body = []byte(strings.TrimPrefix(line, "< body: "))
		case strings.HasPrefix(line, "< "):
			if k, v, ok := strings.Cut(strings.TrimPrefix(line, "< "), ": "); ok {
				resp.Header.Add(k, v)
			}
		}
	}
	if err := scanner.Err(); err != nil || resp.Status == 0 {
		t.Fatalf("sample %s: status %d, %v", name, resp.Status, err)
	}
	return resp
}

// answer is a response made for a test, with the header every new-api answer has.
func answer(status int, body string) adapter.Response {
	return adapter.Response{Status: status, Header: http.Header{requestIDHeader: {"202610150000000000000000test"}}, Body: []byte(body)}
}

// fakeClient answers requests from a table, by "METHOD path", and keeps what it was asked.
type fakeClient struct {
	mu     sync.Mutex
	routes map[string]adapter.Response
	errs   map[string]error
	sent   []adapter.Request
}

func newFake() *fakeClient {
	return &fakeClient{routes: map[string]adapter.Response{}, errs: map[string]error{}}
}

func (f *fakeClient) Do(_ context.Context, r adapter.Request) (adapter.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, r)
	key := r.Method + " " + r.Path
	if err, ok := f.errs[key]; ok {
		return adapter.Response{}, err
	}
	resp, ok := f.routes[key]
	if !ok {
		return adapter.Response{}, fmt.Errorf("fake client: no answer for %s", key)
	}
	return resp, nil
}

// login is a stored login for a made-up token.
type loginStore struct{ raw []byte }

func (s loginStore) Load(context.Context) ([]byte, int64, error) { return s.raw, 1, nil }
func (loginStore) Swap(context.Context, int64, []byte) error     { return nil }

const fakePAT = "nap_test_not_a_real_one_0123456789"

func testLogin(t testing.TB, expires time.Time) loginStore {
	t.Helper()
	raw, err := EncodeLogin(Login{PAT: secret.New(fakePAT), ExpiresAt: expires})
	if err != nil {
		t.Fatal(err)
	}
	return loginStore{raw}
}

// open makes a manager of an adapter whose clock is now, over client c.
func open(t testing.TB, c adapter.Client, now time.Time) adapter.Manager {
	t.Helper()
	m, err := (&Adapter{now: func() time.Time { return now }}).Open(t.Context(), c, testLogin(t, time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// readOffer reads the whole offer over as many reads as it takes, each from the position the one before gave, as the
// module does.
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

// readKeys reads the whole keys listing the same way.
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
