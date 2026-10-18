package gateway

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// The ledger tests run calls through the executor, on fake upstreams, against the real billing tables: what they check
// is the money, which only PostgreSQL holds.

// errAckLost is a write that committed whose acknowledgement did not come back.
var errAckLost = errors.New("the connection broke after the commit")

// flakyLedger is billing with faults: an extension or a settlement can fail before it is written, or be written and
// report a failure, as a commit whose acknowledgement is lost does.
type flakyLedger struct {
	*billing.Billing
	mu     sync.Mutex
	extend func() (write bool, err error)
	settle func() (write bool, err error)
}

func (l *flakyLedger) fault(f func() (bool, error)) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f == nil {
		return true, nil
	}
	return f()
}

func (l *flakyLedger) Extend(ctx context.Context, h *billing.Hold, r billing.ExtendRequest) error {
	write, fail := l.fault(l.extend)
	if !write {
		return fail
	}
	if err := l.Billing.Extend(ctx, h, r); err != nil {
		return err
	}
	return fail
}

func (l *flakyLedger) Settle(ctx context.Context, h *billing.Hold, o billing.Outcome) (billing.Final, error) {
	write, fail := l.fault(l.settle)
	if !write {
		return billing.Final{}, fail
	}
	f, err := l.Billing.Settle(ctx, h, o)
	if err != nil {
		return f, err
	}
	return f, fail
}

// withExecutor gives the rig an executor on the ledger and fake upstreams for its endpoints, which are called in
// Messages and reserved by segment of 100 output tokens.
func withExecutor(r *rig, ledger *flakyLedger) *fakeUpstreams {
	r.t.Helper()
	ledger.Billing = r.billing
	return withCosts(r, ledger, fakeCosts{cost: 1000, known: true})
}

// withCosts is withExecutor with the executor's Costs given: a fixed answer, or the production suppliers service.
func withCosts(r *rig, ledger Ledger, costs Costs) *fakeUpstreams {
	r.t.Helper()
	up := &fakeUpstreams{eps: map[uuid.UUID]*fakeEndpoint{}, client: newClientWriter()}
	for _, e := range r.endpoints {
		up.eps[uuid.UUID(e.ID)] = &fakeEndpoint{policy: suppliers.CallPolicy{Streams: true, Cancels: true}}
	}
	exec, err := NewExecutor(ExecDeps{
		Router: r.router, Upstreams: up, Limits: r.limits, Memory: r.memory, Logger: slog.New(slog.NewTextHandler(logWriter{r.t}, nil)),
		Ledger: ledger, Costs: costs, Alive: r.billing.Alive, IdentitySecret: []byte("0123456789abcdef"),
	})
	if err != nil {
		r.t.Fatal(err)
	}
	r.mu.Lock()
	r.exec = exec
	r.mu.Unlock()
	r.t.Cleanup(func() { exec.Drain(context.WithoutCancel(r.t.Context()), time.Minute) })
	return up
}

func ledgerRig(t *testing.T) *rig {
	return newRig(t, func(o *rigOptions) {
		o.segments = billing.Segments{Output: 100, Overshoot: time.Second}
		o.endpoints = func(_ string, e *routing.Endpoint) {
			e.OutputSpeed, e.Target = 100, protocol.Target{Model: "up"}
		}
	})
}

// stream is a Messages request that streams.
func stream(model string) string {
	return strings.Replace(messageBody(model), `{"model"`, `{"stream":true,"model"`, 1)
}

// streamed is an upstream's answer of n text events of size bytes each, ending normally or broken off, framed by the
// events of the recorded sample.
func streamed(sample []string, n, size int, broken bool) scripted {
	beats := []beat{{data: events(sample, evStart)}, {data: events(sample, evBlock)}}
	for range n {
		beats = append(beats, beat{data: text(size)})
	}
	if broken {
		return serve(append(beats, beat{hangup: true})...)
	}
	return serve(append(beats, beat{data: events(sample, evBlockStop, evMsgDelta, evMsgStop)})...)
}

// TestALostAcknowledgementChargesOnce: a settlement whose commit was not acknowledged is tried again with the same
// outcome, and billing answers it with what it stored: one charge, reported in the ending.
func TestALostAcknowledgementChargesOnce(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	tries := 0
	ledger := &flakyLedger{settle: func() (bool, error) {
		tries++
		if tries == 1 {
			return true, errAckLost
		}
		return true, nil
	}}
	up := withExecutor(r, ledger)
	a := r.funded(1e12)
	sample, _ := sample(t, protocol.Messages)
	for _, e := range up.eps {
		e.script = []scripted{streamed(sample, 3, 50, false)}
	}
	resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")))
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"cost":`) || strings.Contains(body, "billing_pending") {
		t.Fatalf("%d:\n%s", resp.StatusCode, body)
	}
	ledger.mu.Lock()
	n := tries
	ledger.mu.Unlock()
	if n != 2 {
		t.Errorf("%d tries", n)
	}
	if n := r.count("SELECT count(*) FROM billing.transactions WHERE kind = 'settle'"); n != 1 {
		t.Errorf("%d settlements booked", n)
	}
	r.ledgerBalanced()
}

// TestAnEstimateIsNeverChargedBeyondTheReservation: a stream that sent six times the output limit in text and broke
// off without reporting its usage is charged for the limit, which the reservation (extended as the stream went on)
// covers, not for its bytes: settlement guards the balance's floor and nothing else, so what the estimate does not
// cap, a rich account pays.
func TestAnEstimateIsNeverChargedBeyondTheReservation(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	a := r.funded(1e12)
	sample, _ := sample(t, protocol.Messages)
	for _, e := range up.eps {
		e.script = []scripted{streamed(sample, 6, maxOutput, true)}
	}
	resp, body := r.do("POST", "/v1/messages", a.key, strings.NewReader(stream("acme/beta")))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d: %s", resp.StatusCode, body)
	}
	r.exec.Drain(t.Context(), time.Minute)
	if n := r.count("SELECT count(*) FROM billing.reservations WHERE charged > 0"); n != 1 {
		t.Fatalf("%d charges, want the one", n)
	}
	if n := r.count("SELECT count(*) FROM billing.reservations WHERE charged > amount"); n != 0 {
		t.Errorf("%d charges are beyond what was reserved", n)
	}
	r.ledgerBalanced()
}

// TestTheLedgerBalancesWhateverHappens: calls whose upstreams end, break, fail or never run, whose extensions and
// settlements fail or lose their acknowledgements at random, on accounts that run short, leave no reservation pending
// once settled, every balance the sum of its entries and above its floor, and no call charged twice.
func TestTheLedgerBalancesWhateverHappens(t *testing.T) {
	t.Parallel()
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rng := rand.New(rand.NewPCG(seed, 1))    //nolint:gosec // G404: test data
	faults := rand.New(rand.NewPCG(seed, 2)) //nolint:gosec // G404: test data; the ledger's, drawn under its lock
	r := ledgerRig(t)
	sample, _ := sample(t, protocol.Messages)
	fault := func() (bool, error) {
		switch faults.IntN(6) {
		case 0:
			return false, errors.New("connection refused")
		case 1:
			return true, errAckLost
		}
		return true, nil
	}
	ledger := &flakyLedger{extend: fault, settle: fault}
	up := withExecutor(r, ledger)
	accounts := []account{r.funded(1e12), r.funded(3e6), r.funded(1e6)}
	for range 60 {
		var script scripted
		switch rng.IntN(4) {
		case 0:
			script = failure(routing.ScopeEndpoint, rng.IntN(2) == 0)
		default:
			script = streamed(sample, rng.IntN(12), 1+rng.IntN(80), rng.IntN(3) == 0)
		}
		up.mu.Lock()
		for _, e := range up.eps {
			e.script = []scripted{script}
		}
		up.mu.Unlock()
		resp, body := r.do("POST", "/v1/messages", accounts[rng.IntN(len(accounts))].key, strings.NewReader(stream("acme/beta")))
		if resp.StatusCode == 0 || strings.Contains(body, `"cost":`) && strings.Contains(body, "billing_pending") {
			t.Errorf("%d:\n%s", resp.StatusCode, body)
		}
	}
	r.exec.Drain(t.Context(), time.Minute) // the settlements still retrying in the background end
	r.ledgerBalanced()
}

// funded is an account whose balance is a credit on the books, so that the reconciliation has it.
func (r *rig) funded(amount int64) account {
	r.t.Helper()
	a := r.newAccount(0)
	if err := r.db.Tx(r.t.Context(), platformScope, func(tx pg.Tx) error {
		return billing.AddCredit(r.t.Context(), tx, billing.Credit{
			ID: uuid.NewV7(), Site: siteID, Account: a.id, Amount: amount, Actor: audit.System("gateway.test"), Reason: "test",
		})
	}); err != nil {
		r.t.Fatal(err)
	}
	return a
}

// heldByAccount counts the balances whose reserved is not what their reservations still hold: all of a pending one,
// and the rest of an estimate until its correction window ends (billing's own tests hold the same).
const heldByAccount = `SELECT count(*) FROM billing.balances b WHERE b.reserved <> coalesce((SELECT sum(CASE r.state
	WHEN 'pending' THEN r.amount WHEN 'estimated' THEN greatest(r.amount - r.charged, 0) ELSE 0 END)
	FROM billing.reservations r WHERE r.account_id = b.account_id), 0)`

// ledgerBalanced: nothing is pending, what the balances hold is what the reservations do, no balance passed its floor,
// the ledger reconciles, and no call was charged twice.
func (r *rig) ledgerBalanced() {
	r.t.Helper()
	if n := r.count(pendingCount); n != 0 {
		r.t.Errorf("%d reservations still pending", n)
	}
	if n := r.count(heldByAccount); n != 0 {
		r.t.Errorf("%d balances hold other than their reservations", n)
	}
	if n := r.count("SELECT count(*) FROM billing.balances WHERE balance - reserved < floor"); n != 0 {
		r.t.Errorf("%d balances passed their floor", n)
	}
	problems, err := r.billing.Reconcile(r.t.Context())
	if err != nil || len(problems) > 0 {
		r.t.Errorf("reconcile: %v %q", err, problems)
	}
	if n := r.count("SELECT count(*) FROM (SELECT request_id FROM billing.transactions WHERE kind = 'settle' GROUP BY request_id HAVING count(*) > 1) twice"); n != 0 {
		r.t.Errorf("%d calls charged more than once", n)
	}
}
