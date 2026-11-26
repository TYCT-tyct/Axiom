package suppliers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"axiom/internal/platform"
	"axiom/internal/platform/pg"
	"axiom/internal/platform/pgtest"
	"axiom/internal/protocol"
	"axiom/internal/suppliers/internal/adapter"
	"axiom/internal/suppliers/internal/direct"
)

// The direct APIs of these tests are made here, not taken from the providers' packages, so that taking one of those out
// leaves this package building (just adapters-removable): one of Anthropic's family, and one of OpenAI's under a root.
var (
	messagesAPI = direct.New(direct.Provider{Kind: "messagesapi", Family: adapter.FamilyAnthropic, Origin: "https://messages.example"})
	rootedAPI   = direct.New(direct.Provider{Kind: "rootedapi", Family: adapter.FamilyOpenAI, Origin: "https://rooted.example", Root: "/v1beta/openai"})
)

// asDirect makes the world's supplier a provider's own API of the kind, with no balance ever read, as CreateDirect
// leaves one, and restarts the node: a supplier's mode never changes, so a running node's state is the old mode's.
func (w *world) asDirect(kind string) {
	w.t.Helper()
	if err := pgtest.Run(w.t.Context(), w.database.Superuser,
		"UPDATE suppliers.suppliers SET mode = 'direct', kind = '"+kind+"', balance_native = NULL WHERE id = '"+w.supplier.String()+"'"); err != nil {
		w.t.Fatal(err)
	}
	w.restart()
}

// TestOnlyARelayNeedsABalanceToBeRouted: a relay whose balance was never read takes no traffic; a provider's own API has
// none to read, is routed, and any cost bound is payable, until the provider refuses it for want of funds.
func TestOnlyARelayNeedsABalanceToBeRouted(t *testing.T) {
	t.Parallel()
	relay := newWorldWith(t, 4, fakeKind{})
	relay.tx(func(ctx context.Context, tx pg.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE suppliers.suppliers SET balance_native = NULL WHERE id = $1", relay.supplier)
		return err
	})
	relay.restart()
	if routable, err := relay.svc.Routable(); err != nil || len(routable) != 0 {
		t.Fatalf("a relay with no balance read: %d routable endpoints (%v)", len(routable), err)
	}

	direct := newWorldWith(t, 4, fakeKind{}, messagesAPI)
	direct.asDirect("messagesapi")
	routable, err := direct.svc.Routable()
	if err != nil || len(routable) != 1 {
		t.Fatalf("a direct API: %d routable endpoints (%v)", len(routable), err)
	}
	huge, err := platform.ParseExact("1000000000")
	if err != nil {
		t.Fatal(err)
	}
	if f := routable[0].Funds; !f.Unbounded || !f.CanPay(huge) {
		t.Errorf("the direct API's funds %+v cannot pay a cost bound", f)
	}
}

// TestPastedKeysEachKeepTheirOwnMinute: each key pasted for a provider's own API is an account of its own, so two keys
// with three requests a minute each take six, where a relay's two keys of one account take three
// (TestAccountSharesWindowAcrossCredentials).
func TestPastedKeysEachKeepTheirOwnMinute(t *testing.T) {
	t.Parallel()
	w := newWorldWith(t, 8, fakeKind{}, messagesAPI)
	second := w.anotherKey(8)
	w.perMinute(w.credential, 3)
	w.perMinute(second, 3)
	w.asDirect("messagesapi")
	var taken atomic.Int32
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			a, err := w.svc.Acquire(w.endpoint)
			if err != nil {
				if !errors.Is(err, ErrNoSlot) {
					t.Error(err)
				}
				return
			}
			taken.Add(1)
			a.Done()
		})
	}
	wg.Wait()
	if n := taken.Load(); n != 6 {
		t.Fatalf("%d requests in a minute from two keys of three each", n)
	}
}

// TestDirectCallsGoTheProvidersWay: a call to a provider's own API goes to the path it serves the format at, with the
// key in the header it takes, and keeps no request id, since no charge record will ever match one.
func TestDirectCallsGoTheProvidersWay(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind                string
		format              protocol.Format
		path, apiKey, auth  string
		withAnthropicHeader bool
	}{
		{"messagesapi", protocol.Messages, "/v1/messages", "sk-test-key", "", true},
		{"rootedapi", protocol.Chat, "/v1beta/openai/chat/completions", "", "Bearer sk-test-key", false},
	} {
		w := newWorldWith(t, 4, fakeKind{}, messagesAPI, rootedAPI)
		w.asDirect(tc.kind)
		var path, apiKey, auth, version string
		server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			path, apiKey, auth, version = r.URL.Path, r.Header.Get("X-Api-Key"), r.Header.Get("Authorization"), r.Header.Get("Anthropic-Version")
			rw.Header().Set("Request-Id", "req_1")
			rw.Header().Set("X-Request-ID", "req_1")
		}))
		t.Cleanup(server.Close)
		w.pointAt(server.URL)
		status, f, receipt := w.send(tc.format, `{}`)
		if status != http.StatusOK || f != nil {
			t.Fatalf("%s: %d %v", tc.kind, status, f)
		}
		if path != tc.path || apiKey != tc.apiKey || auth != tc.auth || (version != "") != tc.withAnthropicHeader || receipt.ref != "" {
			t.Errorf("%s: sent to %s with key %q, authorization %q, version %q; kept ref %q", tc.kind, path, apiKey, auth, version, receipt.ref)
		}
	}
}
