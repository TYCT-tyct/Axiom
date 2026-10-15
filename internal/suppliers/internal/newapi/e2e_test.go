package newapi

import (
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"axiom/internal/platform/egress"
	"axiom/internal/platform/egresstest"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/client"
)

// TestTheAdapterRunsThroughTheEgressClient: the recorded answers, served over HTTP, reach the adapter through the real
// client and platform/egress: the token goes in the Authorization header, the requests count in the declared buckets,
// and a 429 closes them so that the adapter's next request is not sent at all.
func TestTheAdapterRunsThroughTheEgressClient(t *testing.T) {
	t.Parallel()
	routes := map[string]adapter.Response{
		"GET /api/status":                    sample(t, "01-detect-status"),
		"GET /api/user/login/encryption-key": sample(t, "02-detect-encryption-key"),
		"GET /api/user/self":                 sample(t, "24-self-pat"),
		"GET /api/subscription/self":         sample(t, "45-subscription-self"),
	}
	var served, auth []string
	limited := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.RequestURI()
		served, auth = append(served, key), append(auth, r.Header.Get("Authorization"))
		if limited {
			w.Header().Set("Retry-After", "1200")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		replay(t, w, routes[key])
	}))
	defer server.Close()
	clk := &testClock{t: now}
	a := &Adapter{now: clk.now}
	c, err := client.New(server.URL, egress.NewForTesting(egresstest.Loopback(t)), client.NewLimiter(a.Buckets(), clk.now), clk.now)
	if err != nil {
		t.Fatal(err)
	}
	if detected, err := a.Detect(t.Context(), c); err != nil || detected.Generation != "1.0" {
		t.Fatalf("Detect: %+v, %v", detected, err)
	}
	m := a.managerOf(t, c)
	if account, err := m.Account(t.Context()); err != nil || account.Balance != "5000000" {
		t.Fatalf("Account: %+v, %v", account, err)
	}
	if auth[len(auth)-1] != "Bearer "+fakePAT || auth[0] != "" {
		t.Errorf("the token went to %q and not to the public routes", auth)
	}
	limited = true
	wantBlocked(t, accountErr(t, m), now.Add(1200*time.Second))
	before := len(served)
	wantBlocked(t, accountErr(t, m), now.Add(1200*time.Second))
	if len(served) != before {
		t.Errorf("after a 429 %d requests were sent", len(served)-before)
	}
}

func wantBlocked(t *testing.T, err error, until time.Time) {
	t.Helper()
	if blocked, ok := errors.AsType[adapter.Blocked](err); !ok || !blocked.Until.Equal(until) || !errors.Is(err, adapter.ErrBucketsFull) {
		t.Fatalf("%v, want Blocked until %v", err, until)
	}
}

func accountErr(t *testing.T, m adapter.Manager) error {
	t.Helper()
	_, err := m.Account(t.Context())
	return err
}

// replay writes a recorded response.
func replay(t *testing.T, w http.ResponseWriter, resp adapter.Response) {
	t.Helper()
	if resp.Status == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	maps.Copy(w.Header(), resp.Header)
	w.WriteHeader(resp.Status)
	if _, err := w.Write(resp.Body); err != nil {
		t.Error(err)
	}
}
