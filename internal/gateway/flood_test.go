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
			hotAdmissions = append(hotAdmissions, start(func() (func(), error) { return bp.Funds.Admit(ctx, "hot") }))
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
			start(func() (func(), error) { return bp.Funds.Admit(ctx, "hot") }).mustWait(t)
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
		budget, cancel := context.WithTimeout(ctx, cfg.AdmissionBudget)
		defer cancel()
		admission := start(func() (func(), error) { return bp.Funds.Admit(budget, "a") })
		time.Sleep(cfg.AdmissionBudget)
		if !admission.finished() || !errors.Is(admission.err, context.DeadlineExceeded) {
			t.Fatalf("admission: finished=%v err=%v, want context.DeadlineExceeded", admission.finished(), admission.err)
		}
		mustAcquire(t, func() (func(), error) { return bp.Funds.Complete(ctx, "a") })
	})
}
