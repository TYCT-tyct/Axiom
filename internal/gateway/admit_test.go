package gateway

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"axiom/internal/platform/pg"
	"axiom/internal/routing"
)

// TestAdmissionReservesTheDearestKeptEntry: an admitted call holds one pending reservation, for the dearest entry of
// its route as the quote reads it, and the balance row counts it; the call number is the request ID in the headers.
// When the call ends, nothing is reserved and nothing is held.
func TestAdmissionReservesTheDearestKeptEntry(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	a := r.newAccount(100_000_000)
	release := r.holdCalls()
	answers := together(func() {
		r.checkHeld(r.asked(1)[0])
		release()
	}, func() (reply, string) { return r.message(a, "acme/alpha") })
	resp, body := answers[0].resp, answers[0].body
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	call := r.calls()[0]
	var createdAt time.Time
	if err := r.db.ReadTx(t.Context(), platformScope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), "SELECT created_at FROM billing.reservations WHERE request_id = $1", call.ID).Scan(&createdAt)
	}); err != nil {
		t.Fatal(err)
	}
	if !createdAt.Equal(call.At.Truncate(time.Microsecond)) {
		t.Errorf("the reservation starts at %s, the call at %s; they must be one instant", createdAt, call.At)
	}
	if id := call.ID.String(); resp.Header.Get("X-Request-ID") != id || resp.Header.Get("X-Generation-Id") != id {
		t.Errorf("headers %v do not carry the call number %s", resp.Header, id)
	}
	if r.count(pendingCount) != 0 || r.count(reservedSum) != 0 {
		t.Errorf("after the call %d reservations are pending, %d reserved", r.count(pendingCount), r.count(reservedSum))
	}
	r.consistent()
	r.idle(1 << 30)
}

// checkHeld checks an admitted call that is still running: the one reservation, and the route as the reservation cut it.
func (r *rig) checkHeld(call *Call) {
	r.t.Helper()
	want := quote(messageBody("acme/alpha"), 4000) // alpha-2 charges 4000 a token of output and is the dearer
	if r.count(pendingCount) != 1 || r.count(pendingSum) != want || r.count(reservedSum) != want {
		r.t.Errorf("pending %d, amount %d, balance reserved %d; want one reservation of %d",
			r.count(pendingCount), r.count(pendingSum), r.count(reservedSum), want)
	}
	if got := len(call.Plan.Entries); got != 2 || call.Plan.Entries[0].Endpoint != r.endpoints["alpha-1"].ID {
		r.t.Errorf("the route has %d entries, the first %v", got, call.Plan.Entries)
	}
	if call.Hold == nil || len(call.Plan.Record.Route) != 2 || call.Plan.Record.Route[0] != call.Plan.Entries[0].Endpoint {
		r.t.Errorf("the call: hold %v, route %v", call.Hold, call.Plan.Record.Route)
	}
}

// TestAdmissionTrimsTheRoute: a fallback the account cannot afford is dropped from the route and the reservation is
// for what is left; a preferred entry it cannot afford refuses the request, though a cheaper fallback could be paid.
func TestAdmissionTrimsTheRoute(t *testing.T) {
	t.Parallel()
	enough := quote(messageBody("acme/alpha"), 2000) + 1000 // the cheap entry, not the dear one
	for name, tc := range map[string]struct {
		prefer string
		status int
	}{
		"the dear fallback is dropped":      {"", http.StatusOK},
		"the dear preferred entry is a 402": {"alpha-2", http.StatusPaymentRequired},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, func(o *rigOptions) { o.prefer = tc.prefer })
			a := r.newAccount(enough)
			resp, out := r.message(a, "acme/alpha")
			if resp.StatusCode != tc.status {
				t.Fatalf("%d %s", resp.StatusCode, out)
			}
			if tc.status == http.StatusOK {
				if entries := r.asked(1)[0].Plan.Entries; len(entries) != 1 || entries[0].Endpoint != r.endpoints["alpha-1"].ID {
					t.Errorf("the route after trimming: %v", entries)
				}
			} else if code, typ := errorOf(t, out); code != "credits_insufficient" || typ != "billing_error" || len(r.calls()) != 0 {
				t.Errorf("%s %s, calls %d", code, typ, len(r.calls()))
			}
			r.consistent()
		})
	}
}

// TestConcurrentAdmissionsNeverPassTheBalance: twelve requests at once for an account that can pay for five reserve
// exactly five, the rest are refused 402, and the counters agree with the reservations while they are held.
func TestConcurrentAdmissionsNeverPassTheBalance(t *testing.T) {
	t.Parallel()
	// The gate and the budget would turn the later ones away, which is the hot account's limit and not under test here.
	r := newRig(t, func(o *rigOptions) {
		o.config = func(c *Config) { c.AccountGateWait, c.AdmissionBudget = 30*time.Second, 30*time.Second }
	})
	one := quote(messageBody("acme/beta"), 2000)
	a := r.newAccount(5*one + one/2)
	release := r.holdCalls()
	requests := make([]func() (reply, string), 12)
	var refused atomic.Int64
	for i := range requests {
		requests[i] = func() (reply, string) {
			resp, body := r.message(a, "acme/beta")
			if resp.StatusCode != http.StatusOK {
				refused.Add(1)
			}
			return resp, body
		}
	}
	answers := together(func() {
		r.asked(5)
		for refused.Load() < int64(len(requests)-5) {
			time.Sleep(10 * time.Millisecond)
		}
		if r.count(pendingCount) != 5 || r.count(reservedSum) != 5*one {
			t.Errorf("held: %d reservations, %d reserved; want 5 and %d", r.count(pendingCount), r.count(reservedSum), 5*one)
		}
		r.consistent()
		release()
	}, requests...)
	admitted := 0
	for _, an := range answers {
		switch an.resp.StatusCode {
		case http.StatusOK:
			admitted++
		case http.StatusPaymentRequired:
		default:
			t.Errorf("%d %s", an.resp.StatusCode, an.body)
		}
	}
	if admitted != 5 || len(r.calls()) != 5 {
		t.Errorf("%d admitted, %d reached Run", admitted, len(r.calls()))
	}
	r.consistent()
	r.idle(1 << 30)
}

// TestRefusals: every way the admission can say no leaves no reservation, no call at Run and no slot held, and is
// answered as docs/domains/gateway.md「错误」 says.
func TestRefusals(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		model   string
		options func(*rigOptions)
		change  func(r *rig, a account)
		status  int
		code    string
		typ     string
		retry   string
		named   string // something the body names
	}{
		"a disabled key": {"acme/alpha", nil, func(r *rig, _ account) {
			r.sql("UPDATE accounts.api_keys SET status = 'disabled'")
		}, 401, "authentication", "authentication_error", "", ""},
		"a key whose daily limit is spent": {"acme/alpha", nil, func(r *rig, _ account) {
			r.sql("UPDATE accounts.api_keys SET spend_limit = 1000")
		}, 402, "credits_insufficient", "billing_error", "60", "key_limit"},
		"a workspace budget that is a total": {"acme/alpha", nil, func(r *rig, a account) {
			r.sql("UPDATE accounts.workspaces SET budget = 1000 WHERE id = '" + a.workspace.String() + "'")
		}, 402, "credits_insufficient", "billing_error", "", "workspace_budget"},
		"a model the key may not use": {"acme/alpha", nil, func(r *rig, _ account) {
			r.sql("UPDATE accounts.api_keys SET models = '{acme/beta}'")
		}, 404, "model_not_found", "not_found_error", "", ""},
		"a model that does not exist": {"acme/none", nil, nil, 404, "model_not_found", "not_found_error", "", ""},
		"a model nothing serves":      {"claude-test", nil, nil, 404, "no_endpoint", "not_found_error", "", "no_candidates"},
		"endpoints that cannot take the request": {"acme/alpha", func(o *rigOptions) {
			o.endpoints = func(_ string, e *routing.Endpoint) { e.Native, e.Served = routing.Of(), routing.Of() }
		}, nil, 404, "no_endpoint", "not_found_error", "", "format_not_served"},
		"endpoints whose breakers are open": {"acme/alpha", func(o *rigOptions) {
			o.endpoints = func(_ string, e *routing.Endpoint) { e.BreakerOpen = true }
		}, nil, 503, "no_endpoint", "overloaded_error", "1", "breaker_open"},
		"a routing snapshot gone stale": {
			"acme/alpha", func(o *rigOptions) { o.routing.MaxStale = time.Nanosecond },
			nil, 503, "overloaded", "overloaded_error", "1", "",
		},
		"a holder that was reaped": {"acme/alpha", nil, func(r *rig, _ account) {
			r.sql("UPDATE billing.holders SET reaped_at = now()")
		}, 503, "overloaded", "overloaded_error", "1", ""},
		"an account with no balance row": {"acme/alpha", nil, func(r *rig, a account) {
			r.sql("DELETE FROM billing.balances WHERE account_id = '" + a.id.String() + "'")
		}, 402, "credits_insufficient", "billing_error", "", "balance"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, tc.options)
			a := r.newAccount(100_000_000)
			if tc.change != nil {
				tc.change(r, a)
			}
			resp, body := r.message(a, tc.model)
			code, typ := errorOf(t, body)
			if resp.StatusCode != tc.status || code != tc.code || typ != tc.typ || resp.Header.Get("Retry-After") != tc.retry ||
				!strings.Contains(body, tc.named) {
				t.Errorf("%d %s %s, Retry-After %q, want %d %s %s %q naming %q:\n%s",
					resp.StatusCode, code, typ, resp.Header.Get("Retry-After"), tc.status, tc.code, tc.typ, tc.retry, tc.named, body)
			}
			if len(r.calls()) != 0 || r.count(allRows) != 0 {
				t.Errorf("a refused request left a call or a reservation row behind")
			}
			r.idle(1 << 30)
		})
	}
}

// TestKeyRate: the platform's abuse limit is judged inside the admission from the count taken before it; the
// request over the limit reserves nothing.
func TestKeyRate(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) { o.config = func(c *Config) { c.KeyRate = 2 } })
	a := r.newAccount(100_000_000)
	var codes []int
	for range 3 {
		resp, _ := r.message(a, "acme/beta")
		codes = append(codes, resp.StatusCode)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 429 || r.count(allRows) != 2 {
		t.Errorf("statuses %v, %d reservation rows", codes, r.count(allRows))
	}
	wantRefused(t, r.refused(), map[string]int64{"key_rate": 1})
}
