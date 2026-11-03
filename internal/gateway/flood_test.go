package gateway

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// internal/gateway/README.md G2: a burst of cold-key lookups fills the admission share of the pool while a hot
// account is being settled and more admissions for it pile up. Settlements, for the hot account and for any other,
// must still go through; the hot account's admissions wait for the gate without holding a connection, and give up
// with 429 after the gate wait.
func TestColdKeyFloodAndHotAccountSettlement(t *testing.T) {
	t.Parallel()
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

		settling := holdGate(t, ctx, bp.Funds, "hot")
		var hotAdmissions []*pending
		for range 20 {
			hotAdmissions = append(hotAdmissions, submitGate(ctx, bp.Funds, "hot", Admission, func(context.Context, []*gateItem) error { return nil }))
		}
		synctest.Wait()

		// Waiting at the gate holds no connection: the 3 reserved ones the settlement has not taken are free.
		for range 3 {
			mustAcquire(t, acquire(ctx, bp.Pool, Completion))
		}
		// The hot account's next settlement goes ahead of its 20 waiting admissions, so the settlement of a
		// request is never queued behind the admissions of the account's other requests. It blocks so the
		// admissions stay behind it while their gate wait runs out.
		nextStarted, nextRelease := make(chan struct{}), make(chan struct{})
		nextHot := submitGate(ctx, bp.Funds, "hot", Completion, func(context.Context, []*gateItem) error {
			close(nextStarted)
			<-nextRelease
			return nil
		})
		synctest.Wait() // the completion is queued before the holder goes
		settling()
		synctest.Wait()
		select {
		case <-nextStarted:
		default:
			t.Fatal("the next settlement did not start ahead of the admissions")
		}

		time.Sleep(cfg.AccountGateWait)
		if refused := countRefused(hotAdmissions); refused != len(hotAdmissions) {
			t.Fatalf("%d of %d hot admissions got errAccountGate", refused, len(hotAdmissions))
		}
		close(nextRelease)
		nextHot.mustGranted(t)
	})
}

// A funds transaction waits for its account's gate before it takes a connection.
func TestFundsWaitsForTheGateWithoutAConnection(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		cfg := DefaultConfig()
		bp, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		release := holdGate(t, ctx, bp.Funds, "hot")
		var waiting []*pending
		for range 10 {
			waiting = append(waiting, submitGate(ctx, bp.Funds, "hot", Admission, func(context.Context, []*gateItem) error { return nil }))
		}
		for _, p := range waiting {
			p.mustWait(t)
		}
		var held []func()
		for range cfg.PoolSize - cfg.PoolReserve {
			held = append(held, mustAcquire(t, acquire(ctx, bp.Pool, Admission)))
		}
		release()
		time.Sleep(admissionCollect) // the batch's bounded collection ends and it waits for a connection
		for _, release := range held {
			release()
		}
		for _, p := range waiting {
			p.mustGranted(t)
		}
	})
}

// An admission that runs out of time waiting for a connection gives its account's gate back at once.
func TestFundsReleasesTheGateWhenTheConnectionWaitEnds(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		cfg := DefaultConfig()
		bp, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		for range cfg.PoolSize - cfg.PoolReserve {
			mustAcquire(t, acquire(ctx, bp.Pool, Admission))
		}
		admission := submitGate(ctx, bp.Funds, "a", Admission, func(context.Context, []*gateItem) error { return nil })
		time.Sleep(cfg.AdmissionBudget + time.Millisecond)
		if !admission.finished() || !errors.Is(admission.err, context.DeadlineExceeded) {
			t.Fatalf("admission: finished=%v err=%v, want context.DeadlineExceeded", admission.finished(), admission.err)
		}
		submitGate(ctx, bp.Funds, "a", Completion, func(context.Context, []*gateItem) error { return nil }).mustGranted(t)
	})
}

// An admission queued at its account's gate for longer than the admission budget is not out of time: the budget covers
// the connection and the transaction and starts when the gate is held (issue #295: a burst on one account queues here,
// and a wait counted in the budget could not be longer than the budget).
func TestAdmissionBudgetStartsAtTheGate(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		cfg := DefaultConfig()
		bp, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		release := holdGate(t, ctx, bp.Funds, "a")
		var granted, deadline time.Time
		admission := submitGate(ctx, bp.Funds, "a", Admission, func(ctx context.Context, _ []*gateItem) error {
			granted = time.Now()
			deadline, _ = ctx.Deadline()
			return nil
		})
		time.Sleep(2 * cfg.AdmissionBudget)
		admission.mustWait(t)
		release()
		admission.mustGranted(t)
		if got := deadline.Sub(granted); got != cfg.AdmissionBudget {
			t.Errorf("the budget ends %v after the gate was granted, want %v", got, cfg.AdmissionBudget)
		}
	})
}
