package gateway

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"testing/synctest"
	"time"
)

// internal/gateway/README.md G2: a burst of cold-key lookups fills the admission share of the pool while a hot
// account is being settled and more admissions for it pile up. Settlements, for the hot account and for any other,
// must still go through; the hot account's admissions wait for the gate without holding a connection, and give up
// with 429 after the gate wait.
func TestColdKeyFloodAndHotAccountSettlement(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		cfg := DefaultConfig()
		cfg.PoolSize, cfg.PoolReserve = 12, 4 // admission may use 8
		bp, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}

		var lookups []*pending
		for range 40 {
			lookups = append(lookups, start(acquire(ctx, bp.Pool, Admission)))
		}
		synctest.Wait()
		if granted := countFinished(lookups); granted != 8 {
			t.Fatalf("%d cold-key lookups got a connection, want 8", granted)
		}

		settling := mustAcquire(t, func() (func(), error) { return bp.Funds.Complete(ctx, "hot") })
		var hotAdmissions []*pending
		for range 20 {
			hotAdmissions = append(hotAdmissions, start(admitFunds(ctx, bp.Funds, "hot")))
		}
		synctest.Wait()

		// Waiting at the gate holds no connection: the 3 reserved ones the settlement has not taken are free.
		for i := range 3 {
			mustAcquire(t, func() (func(), error) { return bp.Funds.Complete(ctx, "cold"+strconv.Itoa(i)) })
		}
		// The hot account's next settlement goes ahead of its 20 waiting admissions, so the settlement of a
		// request is never queued behind the admissions of the account's other requests.
		nextHot := start(func() (func(), error) { return bp.Funds.Complete(ctx, "hot") })
		nextHot.mustWait(t)
		settling()
		nextHot.mustGranted(t)

		time.Sleep(cfg.AccountGateWait)
		if refused := countRefused(hotAdmissions); refused != len(hotAdmissions) {
			t.Fatalf("%d of %d hot admissions got errAccountGate", refused, len(hotAdmissions))
		}
	})
}

// A funds transaction waits for its account's gate before it takes a connection.
func TestFundsWaitsForTheGateWithoutAConnection(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		cfg := DefaultConfig()
		bp, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		mustAcquire(t, func() (func(), error) { return bp.Funds.Complete(ctx, "hot") })
		for range 10 {
			start(admitFunds(ctx, bp.Funds, "hot")).mustWait(t)
		}
		for range cfg.PoolSize - cfg.PoolReserve - 1 { // the admission share, less the settlement's connection
			mustAcquire(t, acquire(ctx, bp.Pool, Admission))
		}
	})
}

// An admission that runs out of time waiting for a connection gives its account's gate back at once.
func TestFundsReleasesTheGateWhenTheConnectionWaitEnds(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		cfg := DefaultConfig()
		bp, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		for range cfg.PoolSize - cfg.PoolReserve {
			mustAcquire(t, acquire(ctx, bp.Pool, Admission))
		}
		admission := start(admitFunds(ctx, bp.Funds, "a")) // the budget starts when the gate is held, which it is at once
		time.Sleep(cfg.AdmissionBudget + time.Millisecond)
		if !admission.finished() || !errors.Is(admission.err, context.DeadlineExceeded) {
			t.Fatalf("admission: finished=%v err=%v, want context.DeadlineExceeded", admission.finished(), admission.err)
		}
		mustAcquire(t, func() (func(), error) { return bp.Funds.Complete(ctx, "a") })
	})
}

// An admission queued at its account's gate for longer than the admission budget is not out of time: the budget covers
// the connection and the transaction and starts when the gate is held (issue #295: a burst on one account queues here,
// and a wait counted in the budget could not be longer than the budget).
func TestAdmissionBudgetStartsAtTheGate(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		cfg := DefaultConfig()
		bp, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		holder := mustAcquire(t, func() (func(), error) { return bp.Funds.Complete(ctx, "a") })
		var granted time.Time
		var budget context.Context
		admission := start(func() (func(), error) {
			c, release, err := bp.Funds.Admit(ctx, "a")
			granted, budget = time.Now(), c
			return release, err
		})
		time.Sleep(2 * cfg.AdmissionBudget)
		admission.mustWait(t)
		holder()
		admission.mustGranted(t)
		deadline, ok := budget.Deadline()
		if !ok || deadline.Sub(granted) != cfg.AdmissionBudget {
			t.Errorf("the budget ends %v after the gate was granted, want %v", deadline.Sub(granted), cfg.AdmissionBudget)
		}
	})
}
