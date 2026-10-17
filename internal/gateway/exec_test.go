package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// whole is the recorded stream as the upstream sends it, all at once.
func whole(h *harness) scripted { return serve(beat{data: strings.Join(h.events, "")}) }

// TestACallIsRelayedAndSettledBeforeItsEnding: the client gets 200 and the stream with the id of the call and the model it
// asked for; the settlement runs once after the upstream has ended and before the ending, which carries the cost (I3),
// and the attempt it settles is the committed one.
func TestACallIsRelayedAndSettledBeforeItsEnding(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{final: billing.Final{Charged: platform.AmountExact(3703701)}})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run()
		checkStream(t, h)
		checkSettlementBeforeTheEnding(t, h.ended())
		checkCommittedAttempt(t, h.ended().outcome)
	})
}

func checkStream(t *testing.T, h *harness) {
	t.Helper()
	if h.w.status != http.StatusOK || h.w.header.Get("Content-Type") != "text/event-stream; charset=utf-8" {
		t.Fatalf("status %d, headers %v", h.w.status, h.w.header)
	}
	body := h.w.body()
	for _, want := range []string{`"text": "Hello"`, `"text": "!"`, h.call.ID.String(), `"model": "claude-opus-5-5"`, `"cost":0.003703701,"cost_currency":"USD"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the stream lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "event: ping") {
		t.Error("the upstream's ping before the first content was forwarded")
	}
}

// checkSettlementBeforeTheEnding: when the settlement ran, the content had been forwarded and the ending had not.
func checkSettlementBeforeTheEnding(t *testing.T, s settlement) {
	t.Helper()
	if strings.Contains(s.body, "message_stop") || strings.Contains(s.body, `"cost"`) {
		t.Errorf("the ending was written before the settlement:\n%s", s.body)
	}
	if !strings.Contains(s.body, `"text": "!"`) {
		t.Errorf("the content was not forwarded as it came:\n%s", s.body)
	}
	if s.held != 0 {
		t.Errorf("%d credentials still held at the settlement", s.held)
	}
}

func checkCommittedAttempt(t *testing.T, e billing.Outcome) {
	t.Helper()
	if len(e.Attempts) != 1 || e.Cancelled {
		t.Fatalf("ending %+v", e)
	}
	a := e.Attempts[0]
	if a.Seq != 1 || a.Entry != 0 || a.Result != billing.Committed || a.Finish != billing.FinishStop || len(a.Segments) != 1 ||
		a.Segments[0].Usage.OutputTotal() != 15 {
		t.Errorf("attempt %+v", a)
	}
}

// TestTheUpstreamGetsTheEndpointsRequest: the model is the endpoint's name, the client's session and user identifiers
// are replaced by derived ones, and the whitelisted headers go with it.
func TestTheUpstreamGetsTheEndpointsRequest(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{mutate: func(c *Call) {
			c.Body = []byte(`{"model":"claude-opus-5-5","stream":true,"max_tokens":9,"metadata":{"user_id":"client-secret-user"},"messages":[{"role":"user","content":"hi"}]}`)
			var err error
			if c.Request, err = protocol.Inspect(protocol.Messages, c.Body); err != nil {
				t.Fatal(err)
			}
			c.Request.ServiceTiers = 1 << catalog.ServiceTierStandard
		}})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run()
		call := h.up.endpoint(1).calls[0]
		body := string(call.Body)
		if !strings.Contains(body, `"model":"up-1"`) || strings.Contains(body, "client-secret-user") || !strings.Contains(body, `"user_id":"`) {
			t.Errorf("the request upstream: %s", body)
		}
		if call.Format != protocol.Messages || call.Header.Get("Anthropic-Beta") != "beta-1" {
			t.Errorf("format %d, headers %v", call.Format, call.Header)
		}
		var ids []string
		for _, part := range strings.Split(body, `"user_id":"`)[1:] {
			ids = append(ids, part[:strings.Index(part, `"`)])
		}
		if len(ids) != 1 || len(ids[0]) != 32 {
			t.Errorf("the derived identifier: %v", ids)
		}
	})
}

// TestFailuresMoveTheCallAsRoutingDecides: by the scope of the failure the next entry is tried, the same entry again, or
// the call ends with the client's error; whether the request ran is the supplier's word.
func TestFailuresMoveTheCallAsRoutingDecides(t *testing.T) {
	cases := []struct {
		name        string
		scope       routing.Scope
		notExecuted bool
		wantCalls   [3]int // sends at endpoints 1 and 2 and 3
		wantStatus  int
		result      billing.Result
	}{
		{"an endpoint's failure moves to the next entry", routing.ScopeEndpoint, false, [3]int{1, 1, 0}, 200, billing.Unknown},
		{"a group's too", routing.ScopeGroup, false, [3]int{1, 1, 0}, 200, billing.Unknown},
		{"a supplier's too", routing.ScopeSupplier, true, [3]int{1, 1, 0}, 200, billing.NotExecuted},
		{"a credential's tries the entry again", routing.ScopeCredential, true, [3]int{2, 0, 0}, 200, billing.NotExecuted},
		{"the route to it likewise", routing.ScopeEgress, true, [3]int{2, 0, 0}, 200, billing.NotExecuted},
		{"the request's own is the client's", routing.ScopeRequest, true, [3]int{1, 0, 0}, 400, billing.NotExecuted},
	}
	for _, tc := range cases {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, options{entries: 3})
			h.up.eps[uuidOf(ep(1))].script = []scripted{failure(tc.scope, tc.notExecuted), whole(h)}
			h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
			h.run()
			got := [3]int{h.up.endpoint(1).sends, h.up.endpoint(2).sends, h.up.endpoint(3).sends}
			if got != tc.wantCalls || h.w.status != tc.wantStatus {
				t.Errorf("%s: sends %v, status %d", tc.name, got, h.w.status)
			}
			if first := h.ended().outcome.Attempts[0]; first.Result != tc.result {
				t.Errorf("%s: the first attempt's result %d", tc.name, first.Result)
			}
		})
	}
}

// TestAttemptsAreNumberedBoundAndRecorded: every attempt counts from the moment it is bound, with its number and the
// entry it was priced as; the route's record gets each; and the most is the plan's (never more than three).
func TestAttemptsAreNumberedBoundAndRecorded(t *testing.T) {
	cases := []struct {
		name        string
		entries     int
		maxAttempts int
		want        int
	}{
		{"three entries, all fail", 3, 3, 3},
		{"the plan allows two", 3, 2, 2},
		{"the platform's three is a limit however much the plan allows", 3, 9, 3},
		{"one entry, a credential failing every time", 1, 3, 3},
	}
	for _, tc := range cases {
		run(t, func(t *testing.T, ctx context.Context) {
			scope := routing.ScopeEndpoint
			if tc.entries == 1 {
				scope = routing.ScopeCredential
			}
			h := newHarness(t, ctx, options{entries: tc.entries, maxAttempts: tc.maxAttempts, routing: routing.Config{RetryFloor: 100}})
			for i := 1; i <= tc.entries; i++ {
				h.up.eps[uuidOf(ep(i))].script = []scripted{failure(scope, false)}
			}
			h.run()
			checkNumbered(t, h, tc.name, tc.entries, tc.want)
		})
	}
}

func checkNumbered(t *testing.T, h *harness, name string, entries, want int) {
	t.Helper()
	if h.w.status != http.StatusBadGateway || !strings.Contains(h.w.body(), `"error_type":"upstream_error"`) {
		t.Errorf("%s: status %d:\n%s", name, h.w.status, h.w.body())
	}
	ends := h.ended().outcome.Attempts
	if len(ends) != want {
		t.Fatalf("%s: %d attempts, want %d", name, len(ends), want)
	}
	for i, a := range ends {
		if a.Seq != i+1 || entries > 1 && a.Entry != i {
			t.Errorf("%s: attempt %d is %+v", name, i, a)
		}
	}
	if got := len(h.call.Plan.Record.Attempts); got != want {
		t.Errorf("%s: %d attempts in the route's record", name, got)
	}
}

// TestNothingIsTriedWhenNoEntryCanBe: entries passed over are no attempts. A request no endpoint can express is a 400
// with the reason; credentials that were all taken are a 429 to retry in a second; an endpoint the suppliers do not
// have is the node's own trouble.
func TestNothingIsTriedWhenNoEntryCanBe(t *testing.T) {
	cases := []struct {
		name       string
		o          options
		status     int
		retryAfter string
		reason     routing.Reason
	}{
		{"all taken", options{entries: 2, noSlot: []int{1, 2}}, 429, "1", routing.Full},
		{"an endpoint the suppliers do not hold", options{entries: 1, mutate: func(c *Call) { c.Plan.Entries[0].Endpoint = ep(77) }}, 503, "1", routing.Full},
		{"no endpoint can express it", options{entries: 2, mutate: func(c *Call) {
			for i := range c.Plan.Entries {
				c.Plan.Entries[i].Target.Format = protocol.Chat // a Messages request is not converted to Chat
			}
		}}, 400, "", routing.FormatNotServed},
	}
	for _, tc := range cases {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, tc.o)
			h.run()
			if h.w.status != tc.status || h.w.header.Get("Retry-After") != tc.retryAfter {
				t.Errorf("%s: status %d, Retry-After %q\n%s", tc.name, h.w.status, h.w.header.Get("Retry-After"), h.w.body())
			}
			if s := h.ended(); len(s.outcome.Attempts) != 0 {
				t.Errorf("%s: %d attempts", tc.name, len(s.outcome.Attempts))
			}
			excluded := h.call.Plan.Record.Excluded
			if len(excluded) == 0 || excluded[0].Reason != tc.reason {
				t.Errorf("%s: the route's record: %+v", tc.name, excluded)
			}
		})
	}
}

func TestAMixOfReasonsForPassingOverEntries(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 2, noSlot: []int{1}, mutate: func(c *Call) { c.Plan.Entries[1].Target.Format = protocol.Chat }})
		h.run()
		if h.w.status != 429 { // a credential may be free in a second; the other entry will not ever do
			t.Errorf("status %d\n%s", h.w.status, h.w.body())
		}
	})
}

// TestRetriesSpendTheSuppliersBudget: the first attempt earns it, each later one takes from it, and an entry whose
// supplier's budget is used up is passed over, with the reason in the route's record, while another supplier's goes on.
func TestRetriesSpendTheSuppliersBudget(t *testing.T) {
	cases := []struct {
		name      string
		supplier  func(int) int
		wantSends [3]int
		status    int
	}{
		// A budget of one retry: entry 2 takes it, so entry 3 of the same supplier is passed over.
		{"one supplier twice", func(n int) int { return min(n, 2) }, [3]int{1, 1, 0}, http.StatusBadGateway},
		{"a supplier each", func(n int) int { return n }, [3]int{1, 1, 1}, http.StatusOK},
	}
	for _, tc := range cases {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, options{
				entries: 3, supplier: tc.supplier, routing: routing.Config{RetryFloor: 1, RetryRatio: 0.001},
			})
			h.up.eps[uuidOf(ep(1))].script = []scripted{failure(routing.ScopeEndpoint, false)}
			h.up.eps[uuidOf(ep(2))].script = []scripted{failure(routing.ScopeEndpoint, false)}
			h.up.eps[uuidOf(ep(3))].script = []scripted{whole(h)}
			h.run()
			got := [3]int{h.up.endpoint(1).sends, h.up.endpoint(2).sends, h.up.endpoint(3).sends}
			if got != tc.wantSends || h.w.status != tc.status {
				t.Errorf("%s: sends %v, status %d", tc.name, got, h.w.status)
			}
			excluded := h.call.Plan.Record.Excluded
			if tc.status != http.StatusOK && (len(excluded) != 1 || excluded[0].Reason != routing.RetryBudget || excluded[0].Endpoint != ep(3)) {
				t.Errorf("%s: the route's record: %+v", tc.name, excluded)
			}
		})
	}
}

// TestEveryAttemptIsJournalledWhenBoundAndAnswered: the journal learns of an attempt when it is bound and when the
// response headers arrive, and once more when its content is first handed to the client; a failed attempt never says
// committed.
func TestEveryAttemptIsJournalledWhenBoundAndAnswered(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 2})
		h.up.eps[uuidOf(ep(1))].script = []scripted{failure(routing.ScopeEndpoint, false)}
		h.up.eps[uuidOf(ep(2))].script = []scripted{whole(h)}
		h.run()
		type row struct {
			seq       platform.AttemptSeq
			committed bool
		}
		var got []row
		for _, e := range h.up.journal {
			if e.Request != platform.RequestID(h.call.ID) || e.Site == nil || e.Account == nil {
				t.Fatalf("an entry without the request or the tenant: %+v", e)
			}
			got = append(got, row{e.Seq, e.Committed})
		}
		want := []row{{1, false}, {1, false}, {2, false}, {2, false}, {2, true}}
		if len(got) != len(want) {
			t.Fatalf("journal %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("journal %v, want %v", got, want)
			}
		}
	})
}

// TestJournalLearnsOfTheCommitBeforeTheClientDoes: a crash after the first write must leave the cleanup the attempt that
// was committed, so the journal entry comes first.
func TestJournalLearnsOfTheCommitBeforeTheClientDoes(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		h.up.eps[uuidOf(ep(1))].script = []scripted{whole(h)}
		h.run()
		for i, e := range h.up.journal {
			if e.Committed && h.up.writes[i] != 0 {
				t.Errorf("the commit was journalled after %d writes", h.up.writes[i])
			}
		}
	})
}

// TestAttemptsAreBoundToTheEntriesTheReservationKept: the route is cut to the entries the reservation covers, and an
// attempt is bound to the position its entry had in the route that was quoted.
func TestAttemptsAreBoundToTheEntriesTheReservationKept(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{entries: 3, mutate: func(c *Call) { // the middle entry could not be afforded
			c.Plan.Entries = []routing.Entry{c.Plan.Entries[0], c.Plan.Entries[2]}
			c.Hold.Kept = []int{0, 2}
		}})
		h.up.eps[uuidOf(ep(1))].script = []scripted{failure(routing.ScopeEndpoint, false)}
		h.up.eps[uuidOf(ep(3))].script = []scripted{whole(h)}
		h.run()
		ends := h.ended().outcome.Attempts
		if len(ends) != 2 || ends[0].Entry != 0 || ends[1].Entry != 2 || h.up.endpoint(2).acquired != 0 {
			t.Errorf("attempts %+v", ends)
		}
	})
}

// TestWhenTheHolderMayBeGoneNothingIsStarted, and a request that wants no stream is refused (G8 will take it): both end
// the call by a settlement without an attempt, which releases the reservation.
func TestWhenTheHolderMayBeGoneNothingIsStarted(t *testing.T) {
	no := false
	for _, tc := range []struct {
		name   string
		o      options
		status int
	}{
		{"holder", options{alive: func() bool { return false }}, 503},
		{"no stream", options{stream: &no}, 400},
	} {
		run(t, func(t *testing.T, ctx context.Context) {
			h := newHarness(t, ctx, tc.o)
			h.run()
			if h.w.status != tc.status || h.up.endpoint(1).acquired != 0 || len(h.ended().outcome.Attempts) != 0 {
				t.Errorf("%s: status %d, %d acquired", tc.name, h.w.status, h.up.endpoint(1).acquired)
			}
		})
	}
}

func uuidOf(id routing.EndpointID) suppliers.EndpointID { return suppliers.EndpointID(id) }

// TestAnExecutorNeedsEveryDependency, and a secret long enough to derive identifiers from.
func TestAnExecutorNeedsEveryDependency(t *testing.T) {
	t.Parallel()
	router, err := routing.New(routing.Config{})
	if err != nil {
		t.Fatal(err)
	}
	limits, err := New(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	full := ExecDeps{
		Router: router, Upstreams: &fakeUpstreams{}, Limits: limits, Memory: protocol.NewMemory(1), Logger: slog.New(slog.DiscardHandler),
		Ledger: &fakeLedger{}, Costs: fakeCosts{known: true}, Alive: func() bool { return true },
		IdentitySecret: []byte("0123456789abcdef"),
	}
	if _, err := NewExecutor(full); err != nil {
		t.Fatal(err)
	}
	for name, missing := range map[string]func(*ExecDeps){
		"router": func(d *ExecDeps) { d.Router = nil }, "upstreams": func(d *ExecDeps) { d.Upstreams = nil },
		"limits": func(d *ExecDeps) { d.Limits = nil }, "memory": func(d *ExecDeps) { d.Memory = nil },
		"logger": func(d *ExecDeps) { d.Logger = nil }, "ledger": func(d *ExecDeps) { d.Ledger = nil },
		"costs": func(d *ExecDeps) { d.Costs = nil },
		"alive": func(d *ExecDeps) { d.Alive = nil }, "secret": func(d *ExecDeps) { d.IdentitySecret = []byte("short") },
	} {
		d := full
		missing(&d)
		if _, err := NewExecutor(d); err == nil {
			t.Errorf("an executor without its %s", name)
		}
	}
}

// TestTheSuppliersServiceIsUpstreams: what the service does not give comes back as an error and no connection, not as a
// connection that is nil inside, and the journal is the service's.
func TestTheSuppliersServiceIsUpstreams(t *testing.T) {
	t.Parallel()
	svc, err := suppliers.New(suppliers.Config{})
	if err != nil {
		t.Fatal(err)
	}
	up := NewUpstreams(svc)
	if conn, err := up.Acquire(uuid.New()); conn != nil || !errors.Is(err, suppliers.ErrNoEndpoint) {
		t.Errorf("an endpoint it does not have: %v, %v", conn, err)
	}
	up.Journal(suppliers.JournalEntry{Request: platform.RequestID(uuid.New()), Seq: 1})
	if svc.JournalBuffered() != 1 {
		t.Error("the entry did not reach the service's journal")
	}
}
