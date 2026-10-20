package gateway

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// TestClientAddressBehindATrustedProxy: behind a reverse proxy the deployment names (AXIOM_TRUSTED_PROXIES), the address
// the IP limit counts and the address a key's allow-list judges are the client's as the proxy hands it on, not the
// proxy's; a peer that is not a trusted proxy cannot name anyone.
func TestClientAddressBehindATrustedProxy(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) {
		// The test server's connections come from 127.0.0.1, as a proxy's come from its own network.
		o.proxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("10.0.0.0/8")}
		o.config = func(c *Config) { c.IPInFlight = 2 }
	})
	// Three accounts, since a call in flight takes its account's gate: the two calls kept in flight below and the one
	// that must be refused by the address's count have a key each.
	a, b, c := r.newAccount(1e9), r.newAccount(1e9), r.newAccount(1e9)
	// Every key may be used from one network only, which none of the proxies is in: only the client's own address passes.
	r.sql("UPDATE accounts.api_keys SET ip_allow = '{198.51.100.0/24}'")
	// send serves a request from remote, as the proxy would hand it on.
	send := func(key, remote, forwarded string) *httptest.ResponseRecorder {
		req := post(t, apiHost, "/v1/messages", key, strings.NewReader(messageBody("acme/beta")))
		req.RemoteAddr = remote
		if forwarded != "" {
			req.Header.Set("X-Forwarded-For", forwarded)
		}
		return r.serve(req)
	}
	if rec := send(a.key, "10.9.9.9:4000", "198.51.100.7"); rec.Code != http.StatusOK {
		t.Errorf("a client the key allows, named by a trusted proxy: %d %s", rec.Code, rec.Body)
	}
	if rec := send(a.key, "203.0.113.4:4000", "198.51.100.7"); rec.Code != http.StatusUnauthorized {
		t.Errorf("a peer that is no trusted proxy naming an allowed client: %d %s", rec.Code, rec.Body)
	}
	// Two calls in flight from one client behind the proxy fill its count, and another client of the same proxy has its
	// own: counted by the proxy's address, both clients would share one count of two and the second would be refused.
	admitted := len(r.calls())
	release := r.holdCalls()
	together(func() {
		r.asked(admitted + 2)
		resp, body := r.do(http.MethodPost, "/v1/messages", b.key, strings.NewReader(messageBody("acme/beta")),
			"X-Forwarded-For", "198.51.100.7")
		if code, _ := errorOf(t, body); resp.StatusCode != http.StatusTooManyRequests || code != "rate_limited" {
			t.Errorf("a third call from the client behind the proxy: %d %s", resp.StatusCode, body)
		}
		done := make(chan answer)
		go func() {
			resp, body := r.do(http.MethodPost, "/v1/messages", b.key, strings.NewReader(messageBody("acme/beta")),
				"X-Forwarded-For", "198.51.100.8")
			done <- answer{resp, body}
		}()
		r.asked(admitted + 3)
		release()
		if got := <-done; got.resp.StatusCode != http.StatusOK {
			t.Errorf("another client behind the same proxy: %d %s", got.resp.StatusCode, got.body)
		}
	}, func() (reply, string) {
		return r.do(http.MethodPost, "/v1/messages", a.key, strings.NewReader(messageBody("acme/beta")),
			"X-Forwarded-For", "198.51.100.7")
	}, func() (reply, string) {
		return r.do(http.MethodPost, "/v1/messages", c.key, strings.NewReader(messageBody("acme/beta")),
			"X-Forwarded-For", "198.51.100.7")
	})
	r.idle(1 << 30)
	wantRefused(t, r.refused(), map[string]int64{"ip_in_flight": 1})
}
