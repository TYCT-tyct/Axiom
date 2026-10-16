package gateway

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
	"uuid"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
	"axiom/internal/routing"
)

func (p *Pool) inUse() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.used
}

// waitFor polls until cond holds, or fails.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// lockBalance holds the account's balance row locked for d, as an admission or a settlement of the account does while
// it works, and returns when the lock is held.
func (r *rig) lockBalance(ctx context.Context, a account, d time.Duration) {
	r.t.Helper()
	ctx = context.WithoutCancel(ctx)
	scope := pg.NewScope(false, nil, []uuid.UUID{a.id})
	locked := make(chan struct{})
	go func() {
		err := r.db.Tx(ctx, scope, func(tx pg.Tx) error {
			if _, err := tx.Exec(ctx, "SELECT 1 FROM billing.balances WHERE account_id = $1 FOR UPDATE", a.id); err != nil {
				return err
			}
			close(locked)
			time.Sleep(d)
			return nil
		})
		if err != nil {
			r.t.Error(err)
		}
	}()
	<-locked
}

// TestQueuedAdmissionsHoldNoConnection: with the endpoint full, more requests than the pool has connections for wait
// in the queue holding none (accounts README「准入事务里不等待」); another tenant's admission and settlement go through
// meanwhile, and a freed slot lets the queue through.
func TestQueuedAdmissionsHoldNoConnection(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) {
		// A budget of a second would turn away admissions that wait their turn for one of the six connections on a busy machine.
		o.config = func(c *Config) {
			c.PoolSize, c.PoolReserve, c.AdmissionBudget, c.QueueWait = 8, 2, 60*time.Second, 100*time.Second
		}
		o.endpoints = func(name string, e *routing.Endpoint) {
			if name == "beta-1" {
				e.MaxConcurrent = 1
			}
		}
	})
	other := r.newAccount(1e9)
	beta := r.endpoints["beta-1"].ID
	r.router.Begin(beta)                           // the endpoint is full
	requests := make([]func() (reply, string), 12) // one account each: one account's gate would turn some away
	for i := range requests {
		waiting := r.newAccount(1e9)
		requests[i] = func() (reply, string) { return r.message(waiting, "acme/beta") }
	}
	answers := together(func() {
		waitFor(t, "the queue to fill", func() bool { return r.limits.Queue.queued() == len(requests) })
		if used := r.limits.Pool.inUse(); used != 0 {
			t.Errorf("%d connections are held by requests that are waiting in the queue", used)
		}
		if resp, body := r.message(other, "acme/alpha"); resp.StatusCode != http.StatusOK {
			t.Errorf("another model's admission and settlement: %d: %s", resp.StatusCode, body)
		}
		r.router.End(beta)
		for range requests {
			r.limits.Queue.Wake()
		}
	}, requests...)
	for _, an := range answers {
		if an.resp.StatusCode != http.StatusOK {
			t.Errorf("%d %s", an.resp.StatusCode, an.body)
		}
	}
	r.consistent()
	r.idle(1 << 30)
}

// TestQueueLimits: the queue turns away the waiters it has no room for at once, and the others when the wait is over,
// with the router's estimate of the wait; nothing is reserved and nothing stays queued.
func TestQueueLimits(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) {
		o.config = func(c *Config) { c.QueuePerKey, c.QueueWait = 2, 400*time.Millisecond }
		o.endpoints = func(name string, e *routing.Endpoint) {
			if name == "beta-1" {
				e.MaxConcurrent = 1
			}
		}
	})
	a := r.newAccount(1e9)
	r.router.Begin(r.endpoints["beta-1"].ID)
	requests := make([]func() (reply, string), 3)
	for i := range requests {
		requests[i] = func() (reply, string) { return r.message(a, "acme/beta") }
	}
	for i, an := range together(func() {}, requests...) {
		code, _ := errorOf(t, an.body)
		if an.resp.StatusCode != http.StatusTooManyRequests || code != "rate_limited" || an.resp.Header.Get("Retry-After") != "1" {
			t.Errorf("request %d: %d %s, Retry-After %q", i, an.resp.StatusCode, an.body, an.resp.Header.Get("Retry-After"))
		}
	}
	if r.limits.Queue.queued() != 0 || r.count(allRows) != 0 || len(r.calls()) != 0 {
		t.Errorf("%d still queued, %d reservation rows, %d calls", r.limits.Queue.queued(), r.count(allRows), len(r.calls()))
	}
	r.idle(1 << 30)
}

type admitFunc = func(context.Context, accounts.Caller, accounts.AdmitRequest, func(pg.Tx, accounts.Admission) error) error

var errDropped = errors.New("connection reset by peer")

// lostAfterCommit is an admission that commits and then loses the connection before the acknowledgement.
func (r *rig) lostAfterCommit() admitFunc {
	return func(ctx context.Context, c accounts.Caller, req accounts.AdmitRequest, fn func(pg.Tx, accounts.Admission) error) error {
		if err := r.accounts.Admit(ctx, c, req, fn); err != nil {
			return err
		}
		return errDropped
	}
}

// failedCommit is an admission whose commit fails: the transaction is rolled back after its reservation was written.
func (r *rig) failedCommit() admitFunc {
	return func(ctx context.Context, c accounts.Caller, req accounts.AdmitRequest, fn func(pg.Tx, accounts.Admission) error) error {
		return r.accounts.Admit(ctx, c, req, func(tx pg.Tx, a accounts.Admission) error {
			if err := fn(tx, a); err != nil {
				return err
			}
			return errDropped
		})
	}
}

// slowAfter locks the account's balance for d once admit has returned, so the settlement that follows has to wait.
func (r *rig) slowAfter(admit admitFunc, a account, d time.Duration) admitFunc {
	return func(ctx context.Context, c accounts.Caller, req accounts.AdmitRequest, fn func(pg.Tx, accounts.Admission) error) error {
		err := admit(ctx, c, req, fn)
		r.lockBalance(ctx, a, d)
		return err
	}
}

// TestLostCommit: when the admission's COMMIT is not acknowledged the request is answered 503 and not sent upstream,
// and SettleOrphan leaves the database right whichever way the commit went: a reservation that was committed is
// released with no attempt, and one that was rolled back leaves a tombstone, so the admission can never land later.
// The settlement is retried when the database is slow, and holds the call's slots until it is done.
func TestLostCommit(t *testing.T) {
	t.Parallel()
	reserved := quote(messageBody("acme/beta"), 2000)
	for name, tc := range map[string]struct {
		lose   func(r *rig, a account) admitFunc
		amount int64 // of the one released row left
	}{
		"committed":   {func(r *rig, _ account) admitFunc { return r.lostAfterCommit() }, reserved},
		"rolled back": {func(r *rig, _ account) admitFunc { return r.failedCommit() }, 0},
		"committed, and the database slow to settle": {func(r *rig, a account) admitFunc {
			return r.slowAfter(r.lostAfterCommit(), a, 2500*time.Millisecond)
		}, reserved},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			a := r.newAccount(1e9)
			r.handler.admitTx = tc.lose(r, a)
			resp, body := r.message(a, "acme/beta")
			if code, _ := errorOf(t, body); resp.StatusCode != http.StatusServiceUnavailable || code != "overloaded" || len(r.calls()) != 0 {
				t.Fatalf("%d %s, %d calls", resp.StatusCode, body, len(r.calls()))
			}
			r.handler.Drain(t.Context())
			if r.count(allRows) != 1 || r.count("SELECT count(*) FROM billing.reservations WHERE state = 'released' AND amount = $1", tc.amount) != 1 {
				t.Errorf("%d reservation rows, want one released with %d", r.count(allRows), tc.amount)
			}
			if r.count(pendingCount) != 0 || r.count(reservedSum) != 0 {
				t.Errorf("%d pending, %d reserved after the settlement", r.count(pendingCount), r.count(reservedSum))
			}
			r.idle(1 << 30)
		})
	}
}

// TestAdmissionGivesUpWhenTheBalanceIsLocked: an admission that cannot get the balance row inside its budget is
// answered 503 and leaves nothing; once the row is free the next one is admitted.
func TestAdmissionGivesUpWhenTheBalanceIsLocked(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *rigOptions) {
		o.config = func(c *Config) {
			c.AdmissionBudget, c.LockTimeout, c.StatementTimeout = 500*time.Millisecond, 200*time.Millisecond, 300*time.Millisecond
		}
	})
	a := r.newAccount(1e9)
	r.lockBalance(t.Context(), a, 8*time.Second)
	start := time.Now()
	resp, body := r.message(a, "acme/beta")
	if code, _ := errorOf(t, body); resp.StatusCode != http.StatusServiceUnavailable || code != "overloaded" || resp.Header.Get("Retry-After") != "1" ||
		time.Since(start) > 6*time.Second { // it gave up at its budget of half a second, not when the lock was freed
		t.Errorf("%d %s after %v", resp.StatusCode, body, time.Since(start))
	}
	if r.count(allRows) != 0 || len(r.calls()) != 0 {
		t.Errorf("%d reservation rows, %d calls", r.count(allRows), len(r.calls()))
	}
	r.idle(1 << 30)
	waitFor(t, "the lock to be released", func() bool {
		time.Sleep(200 * time.Millisecond)
		resp, _ := r.message(a, "acme/beta")
		return resp.StatusCode == http.StatusOK
	})
}
