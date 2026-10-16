package suppliers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
)

// TestSlotsAreTakenOnceAndGivenBackOnce: a credential with two slots serves two attempts at once whoever asks, the third
// finds none, and Done frees a slot once however often it is called; a snapshot rebuilt while attempts run does not
// forget them.
func TestSlotsAreTakenOnceAndGivenBackOnce(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 2)
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("a third attempt on two slots: %v", err)
	}
	if err := w.svc.Reload(t.Context()); err != nil { // rebuilt under load
		t.Fatal(err)
	}
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("after a rebuild the full credential has free slots: %v", err)
	}
	for range 3 {
		a.Done() // however often
	}
	c, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatalf("after Done: %v", err)
	}
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Errorf("Done three times freed more than one slot: %v", err)
	}
	b.Done()
	c.Done()
	if _, err := w.svc.Acquire(uuidOf(1)); !errors.Is(err, ErrNoEndpoint) {
		t.Errorf("an endpoint that is not there: %v", err)
	}
}

// TestNeverMoreAttemptsThanSlotsWhoeverRaces: many goroutines acquiring and finishing never hold more slots at once
// than the credential has.
func TestNeverMoreAttemptsThanSlotsWhoeverRaces(t *testing.T) {
	t.Parallel()
	const slots = 5
	w := newWorld(t, slots)
	var held, peak atomic.Int32
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			for range 200 {
				a, err := w.svc.Acquire(w.endpoint)
				if err != nil {
					continue
				}
				n := held.Add(1)
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
				held.Add(-1)
				a.Done()
			}
		})
	}
	wg.Wait()
	if peak.Load() > slots || peak.Load() == 0 {
		t.Errorf("peak of %d attempts at once on %d slots", peak.Load(), slots)
	}
}

// TestACredentialRestsWhenTheSupplierRefusesIt and an expired one is not picked, by the clock and not by a change in
// the database.
func TestACredentialRestsWhenTheSupplierRefusesIt(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	status := atomic.Int32{}
	status.Store(429)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) { rw.WriteHeader(int(status.Load())) }))
	defer server.Close()
	w.pointAt(server.URL)
	send := func() *Failure {
		a, err := w.svc.Acquire(w.endpoint)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Done()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		resp, f := a.Send(ctx, Call{Format: protocol.Chat, Body: []byte(`{}`)})
		if resp != nil {
			_ = resp.Body.Close() //nolint:errcheck // the answers here are failures
		}
		return f
	}
	if f := send(); f == nil || f.Scope != adapter.ScopeCredential {
		t.Fatalf("a 429: %+v", f)
	}
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("a credential asked to wait three seconds is picked at once: %v", err)
	}
	w.clock.advance(4 * time.Second)
	status.Store(403) // allowance used up: rests for the configured quota cooldown
	if f := send(); f == nil {
		t.Fatal("no failure")
	}
	w.clock.advance(w.svc.cfg.QuotaCooldown - time.Second)
	if _, err := w.svc.Acquire(w.endpoint); !errors.Is(err, ErrNoSlot) {
		t.Fatalf("a credential whose allowance is used up is picked before the cooldown: %v", err)
	}
	w.clock.advance(2 * time.Second)
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatalf("after the cooldown: %v", err)
	}
	a.Done()
}

// supplierServer is a made-up supplier that answers by what the body of the request says, and keeps the last request.
type supplierServer struct {
	*httptest.Server
	mu                      sync.Mutex
	path, auth, version, ct string
	body                    []byte
}

func newSupplierServer(t *testing.T) *supplierServer {
	t.Helper()
	s := &supplierServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) //nolint:errcheck // a test server
		s.mu.Lock()
		s.path, s.auth, s.version, s.ct, s.body = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Anthropic-Version"), r.Header.Get("Content-Type"), body
		s.mu.Unlock()
		switch string(body) {
		case "hangup":
			if conn, _, err := rw.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close() //nolint:errcheck // the point
			}
		case "refused":
			rw.Header().Set("X-Ref", "ref-of-a-refusal")
			rw.WriteHeader(http.StatusUnauthorized)
		case "failed":
			rw.WriteHeader(http.StatusInternalServerError)
		default:
			rw.Header().Set("X-Ref", "ref-1")
			_, _ = rw.Write([]byte("ok")) //nolint:errcheck // a test server
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// send runs one attempt of the world's endpoint: its status if there was a response, its failure if not, its receipt.
func (w *world) send(format protocol.Format, body string) (status int, f *Failure, receipt AttemptReceipt) {
	w.t.Helper()
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		w.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(w.t.Context(), 10*time.Second)
	defer cancel()
	resp, f := a.Send(ctx, Call{Format: format, Body: []byte(body)})
	if resp != nil {
		status = resp.StatusCode
		if err := resp.Body.Close(); err != nil {
			w.t.Error(err)
		}
	}
	return status, f, a.Done()
}

// TestASuccessfulSendIsAuthenticatedAndKeepsTheKey: the key goes in Authorization, the path is the format's, Anthropic's
// version header is added for its format, and the receipt has the supplier's request id.
func TestASuccessfulSendIsAuthenticatedAndKeepsTheKey(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	server := newSupplierServer(t)
	w.pointAt(server.URL)
	status, f, receipt := w.send(protocol.Messages, `{"m":1}`)
	if f != nil || status != http.StatusOK {
		t.Fatalf("a success: %d, %+v", status, f)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.path != "/v1/messages" || server.auth != "Bearer sk-test-key" || server.version == "" || server.ct != "application/json" || string(server.body) != `{"m":1}` {
		t.Errorf("the request: %q %q %q %q %s", server.path, server.auth, server.version, server.ct, server.body)
	}
	if receipt.ref != "ref-1" || receipt.credential != w.credential || receipt.endpoint != w.endpoint || receipt.schedule != w.sc {
		t.Errorf("receipt %+v", receipt)
	}
}

// TestSendDecidesWhetherTheSupplierRanTheRequest: before the request is fully written nothing ran; after it, only the
// adapter's word counts, so a hangup after the write and a 5xx are unknown, and a refusal the adapter knows is not
// executed. The supplier's request id is kept from any answer, failed or not.
func TestSendDecidesWhetherTheSupplierRanTheRequest(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	server := newSupplierServer(t)
	w.pointAt(server.URL)
	cases := []struct {
		name, body string
		want       adapter.Failure
		ref        string
	}{
		{"refused by the supplier before running", "refused", adapter.Failure{Executed: adapter.NotExecuted, Scope: adapter.ScopeCredential, Cause: adapter.CredentialRejected{}}, "ref-of-a-refusal"},
		{"a 5xx may have run", "failed", adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.UpstreamError{Status: 500}}, ""},
		{"a hangup after the request was written may have run", "hangup", adapter.Failure{Scope: adapter.ScopeEndpoint, Cause: adapter.Transport{}}, ""},
	}
	for _, c := range cases {
		w.clock.advance(time.Hour) // a refusal rests the credential
		_, f, receipt := w.send(protocol.Chat, c.body)
		if f == nil || cmp.Diff(c.want, *f) != "" || receipt.ref != c.ref {
			t.Errorf("%s: %+v with ref %q, want %+v with %q", c.name, f, receipt.ref, c.want, c.ref)
		}
	}
	// A server that is not there: nothing was written, so nothing ran and the fault is on the way.
	server.Close()
	if _, f, _ := w.send(protocol.Chat, "ok"); f == nil || f.Executed != adapter.NotExecuted || f.Scope != adapter.ScopeEgress || f.Cause != (adapter.Transport{}) {
		t.Errorf("a refused connection: %+v", f)
	}
}

// TestMeasuredCancellationReplacesTheDefault: an endpoint behaves as its adapter says until quality has measured it.
func TestMeasuredCancellationReplacesTheDefault(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Done()
	if p := a.Policy(); !p.Streams || !p.Cancels {
		t.Fatalf("the adapter's default: %+v", p)
	}
	w.svc.SetMeasuredCancels(w.endpoint, false)
	if p := a.Policy(); !p.Streams || p.Cancels {
		t.Errorf("after measuring that it does not cancel: %+v", p)
	}
}

// TestCostPricesUsageWithTheReceiptsSchedule: 100 in and 50 out at 0.075 and 0.3 is 22.5 quota, summed over segments;
// an item with no price is flagged and left out; no usage is flagged and zero; a schedule not held prices nothing and
// says so.
func TestCostPricesUsageWithTheReceiptsSchedule(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	receipt := a.Done()
	usage := func(counts map[catalog.Item]int64) catalog.Usage {
		u, err := catalog.NewUsage(counts, catalog.ServiceTierStandard, nil)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	cases := []struct {
		name  string
		usage []catalog.Usage
		want  string
		flag  CostFlag
	}{
		{"one segment", []catalog.Usage{usage(map[catalog.Item]int64{catalog.ItemInput: 100, catalog.ItemOutput: 50})}, "22.5", CostComplete},
		{"two segments", []catalog.Usage{usage(map[catalog.Item]int64{catalog.ItemInput: 100}), usage(map[catalog.Item]int64{catalog.ItemOutput: 50})}, "22.5", CostComplete},
		{"an item with no price", []catalog.Usage{usage(map[catalog.Item]int64{catalog.ItemInput: 100, catalog.ItemCacheRead: 10})}, "7.5", CostUnpriced},
		{"no usage", nil, "0", CostNoUsage},
		{"zero usage", []catalog.Usage{usage(nil)}, "0", CostNoUsage},
	}
	for _, c := range cases {
		got, err := w.svc.Cost(receipt, c.usage)
		text, derr := decimal(got.Native)
		if err != nil || derr != nil || text != c.want || got.Flag != c.flag {
			t.Errorf("%s: %q, %v, %v; want %q, %v", c.name, text, got.Flag, err, c.want, c.flag)
		}
	}
	unknown := receipt
	unknown.schedule = uuidOf(9)
	got, err := w.svc.Cost(unknown, []catalog.Usage{usage(map[catalog.Item]int64{catalog.ItemInput: 100})})
	if err != nil || got.Flag != CostUnpriced || !got.Missing.Has(catalog.ItemInput) {
		t.Errorf("a schedule not held: %+v, %v", got, err)
	}
}
