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
	"testing/synctest"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform/audit"
	"axiom/internal/platform/pg"
	"axiom/internal/protocol"
	"axiom/internal/quality"
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
	mu        sync.Mutex
	extend    func() (write bool, err error)
	settle    func() (write bool, err error)
	commitErr error // a fault of a write that did commit, reported after the batch's commit
}

func (l *flakyLedger) fault(f func() (bool, error)) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f == nil {
		return true, nil
	}
	return f()
}

func (l *flakyLedger) ExtendIn(ctx context.Context, tx pg.Tx, h *billing.Hold, r billing.ExtendRequest) error {
	write, fail := l.fault(l.extend)
	if !write {
		return fail
	}
	if err := l.Billing.ExtendIn(ctx, tx, h, r); err != nil {
		return err
	}
	l.lost(fail)
	return nil
}

func (l *flakyLedger) SettleIn(ctx context.Context, tx pg.Tx, h *billing.Hold, o billing.Outcome) (billing.Final, func(), error) {
	write, fail := l.fault(l.settle)
	if !write {
		return billing.Final{}, nil, fail
	}
	f, after, err := l.Billing.SettleIn(ctx, tx, h, o)
	if err != nil {
		return f, nil, err
	}
	l.lost(fail)
	return f, after, nil
}

// lost remembers a fault that happened after the write: it is reported when the batch's commit returns, as a commit
// whose acknowledgement was lost is.
func (l *flakyLedger) lost(err error) {
	if err == nil {
		return
	}
	l.mu.Lock()
	l.commitErr = err
	l.mu.Unlock()
}

func (l *flakyLedger) RunBatch(ctx context.Context, scope pg.Scope, works []func(pg.Tx) error) error {
	l.mu.Lock()
	l.commitErr = nil
	l.mu.Unlock()
	if err := l.Billing.RunBatch(ctx, scope, works); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.commitErr
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
		Ledger: ledger, Costs: costs, Alive: r.billing.Alive, IdentitySecret: []byte("0123456789abcdef"), Quality: quality.New(nil),
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
		o.config = func(c *Config) { c.SegmentOutput, c.SegmentOvershoot = 100, time.Second }
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

// heldByAccount counts the balances whose reserved is not what their reservations still hold: all of a pending one
// (billing's own tests hold the same).
const heldByAccount = `SELECT count(*) FROM billing.balances b WHERE b.reserved <> coalesce((SELECT sum(CASE r.state
	WHEN 'pending' THEN r.amount ELSE 0 END) FROM billing.reservations r WHERE r.account_id = b.account_id), 0)`

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
	problems, err := billing.Reconcile(r.t.Context(), r.db, platformScope)
	if err != nil || len(problems) > 0 {
		r.t.Errorf("reconcile: %v %q", err, problems)
	}
	if n := r.count("SELECT count(*) FROM (SELECT request_id FROM billing.transactions WHERE kind = 'settle' GROUP BY request_id HAVING count(*) > 1) twice"); n != 0 {
		r.t.Errorf("%d calls charged more than once", n)
	}
}

// TestAFailureIsNotChargedForModelUsageBecauseTheClientLeftAfterIt: the first attempt fails with nothing delivered, and the
// client leaves before the second can start. The charged attempt is the failed one, so the call is judged by how it failed,
// not by the leaving: its model usage is waived and the account is charged nothing (docs/domains/billing.md「免单规则」).
func TestAFailureIsNotChargedForModelUsageBecauseTheClientLeftAfterIt(t *testing.T) {
	t.Parallel()
	r := ledgerRig(t)
	up := withExecutor(r, &flakyLedger{})
	a := r.funded(1e12)
	first, second := uuid.UUID(r.endpoints["alpha-1"].ID), uuid.UUID(r.endpoints["alpha-2"].ID)
	up.eps[first].script = []scripted{failure(routing.ScopeEndpoint, false)}
	up.eps[second].noSlot = true // the second entry is passed over, as the client leaves as it is asked for
	ctx, leave := context.WithCancel(t.Context())
	defer leave()
	up.beforeAcquire = func(id suppliers.EndpointID) {
		if id == second {
			leave()
			// The server learns the client is gone from its connection, a moment after the client closes it: the call
			// must have heard before it concludes, or this tests nothing.
			r.mu.Lock()
			served := r.clientGone
			r.mu.Unlock()
			select {
			case <-served:
			case <-time.After(30 * time.Second):
				t.Error("the server never saw the client leave")
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.server.URL+"/v1/messages", strings.NewReader(messageBody("acme/alpha")))
	if err != nil {
		t.Fatal(err)
	}
	req.Host, req.Header = apiHost, http.Header{"Authorization": {"Bearer " + a.key}}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		status := resp.StatusCode
		if err := resp.Body.Close(); err != nil {
			t.Error(err)
		}
		t.Fatalf("the client was answered with %d: it was to have left", status)
	}
	deadline := time.Now().Add(30 * time.Second)
	for r.count("SELECT count(*) FROM billing.reservations WHERE state = 'settled'") != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the call was never settled")
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.exec.Drain(t.Context(), time.Minute)
	if n := r.count("SELECT count(*) FROM billing.reservations WHERE state = 'settled' AND waived AND charged = 0"); n != 1 {
		t.Errorf("%d reservations settled as waived and charged nothing, want the one: the client left after the failure", n)
	}
	r.ledgerBalanced()
}

// TestCompletionBatchKeepsOneWritesFailureOffTheRest: one extension's key limit rolls the batch back, but only that
// request gets the error; the other extension is retried in its own transaction, runs its report, and does not cut a
// stream. The same withFunds path carries the settle and extend batches, so this covers the gateway's retry of the
// members that did not fail, not a manual single-request retry.
func TestCompletionBatchKeepsOneWritesFailureOffTheRest(t *testing.T) {
	testOneCompletionFailure(t, &billing.Insufficient{Source: billing.SourceKey})
}

// TestCompletionBatchKeepsAMissingReservationOffTheRest is the settlement shape of the same bug: pg.ErrNoRows is
// permanent, so when it was copied to a normal member, permanent() discarded that member's settlement and only the
// cleanup released its reservation. Here the normal member still commits and charges.
func TestCompletionBatchKeepsAMissingReservationOffTheRest(t *testing.T) {
	testOneCompletionFailure(t, pg.ErrNoRows)
}

func testOneCompletionFailure(t *testing.T, fail error) {
	t.Helper()
	run(t, func(t *testing.T, ctx context.Context) {
		h := newHarness(t, ctx, options{})
		release := holdGate(t, ctx, h.exec.Limits.Funds, accountKey(h.call.caller))
		first := &Call{ID: h.call.ID, Admitted: h.call.Admitted, caller: h.call.caller}
		second := &Call{ID: uuid.New(), Admitted: h.call.Admitted, caller: h.call.caller}
		var ran, reported bool
		firstDone := make(chan error, 1)
		secondStarted, releaseSecond := make(chan struct{}), make(chan struct{})
		secondDone := make(chan error, 1)
		go func() {
			firstDone <- h.exec.withFunds(ctx, first, func(context.Context, pg.Tx) (func(), error) {
				return nil, fail
			})
		}()
		synctest.Wait()
		go func() {
			secondDone <- h.exec.withFunds(ctx, second, func(context.Context, pg.Tx) (func(), error) {
				close(secondStarted)
				<-releaseSecond
				ran = true
				return func() { reported = true }, nil
			})
		}()
		synctest.Wait()
		release()
		<-secondStarted
		synctest.Wait()
		var firstErr error
		select {
		case firstErr = <-firstDone:
		default:
			close(releaseSecond)
			t.Fatal("the failed request waited for the retry of the rest of the batch")
		}
		if !errors.Is(firstErr, fail) {
			close(releaseSecond)
			t.Fatalf("failed request got %v, want %v", firstErr, fail)
		}
		close(releaseSecond)
		secondErr := <-secondDone
		if secondErr != nil || !ran || !reported {
			t.Fatalf("second request got %v, ran=%v reported=%v; want success and its report", secondErr, ran, reported)
		}
	})
}
