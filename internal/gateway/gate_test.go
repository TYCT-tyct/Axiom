package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func enter(ctx context.Context, g *accountGate, account string, class Class) func() (func(), error) {
	return func() (func(), error) { return g.enter(ctx, account, class) }
}

// A settlement that arrives after an admission still gets the account first.
func TestGateCompletionOvertakesWaitingAdmissions(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		g := newAccountGate(200 * time.Millisecond)
		holder := mustAcquire(t, enter(ctx, g, "a", Admission))
		admission := start(enter(ctx, g, "a", Admission))
		admission.mustWait(t)
		completion := start(enter(ctx, g, "a", Completion))
		completion.mustWait(t)

		holder()
		completion.mustGranted(t)
		admission.mustWait(t)
		completion.release()
		admission.mustGranted(t)
	})
}

func TestGateAdmissionGivesUpAfterTheWait(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		g := newAccountGate(200 * time.Millisecond)
		holder := mustAcquire(t, enter(ctx, g, "a", Completion))
		admission := start(enter(ctx, g, "a", Admission))
		time.Sleep(199 * time.Millisecond)
		admission.mustWait(t)
		time.Sleep(time.Millisecond)
		if !admission.finished() || !errors.Is(admission.err, ErrRateLimited) {
			t.Fatalf("after 200ms: finished=%v err=%v, want ErrRateLimited", admission.finished(), admission.err)
		}

		// The waiter left no trace: when the holder goes, the next caller gets the gate at once and the account is
		// forgotten afterwards.
		holder()
		mustAcquire(t, enter(ctx, g, "a", Admission))()
		if len(g.accounts) != 0 {
			t.Fatalf("%d accounts left in the gate map", len(g.accounts))
		}
	})
}

// A completion carries money already committed to, so it has no wait limit of its own; a client that goes away
// leaves with its own error, not a 429.
func TestGateCompletionKeepsWaitingAndClientCancelIsNotARefusal(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		g := newAccountGate(200 * time.Millisecond)
		holder := mustAcquire(t, enter(ctx, g, "a", Completion))
		completion := start(enter(ctx, g, "a", Completion))
		clientCtx, cancel := context.WithCancel(ctx)
		admission := start(func() (func(), error) { return g.enter(clientCtx, "a", Admission) })

		time.Sleep(time.Minute)
		if admission.finished() == false || !errors.Is(admission.err, ErrRateLimited) {
			t.Fatalf("admission: finished=%v err=%v, want ErrRateLimited", admission.finished(), admission.err)
		}
		completion.mustWait(t)

		admission = start(func() (func(), error) { return g.enter(clientCtx, "a", Admission) })
		admission.mustWait(t)
		cancel()
		if !admission.finished() || !errors.Is(admission.err, context.Canceled) {
			t.Fatalf("cancelled admission: finished=%v err=%v, want context.Canceled", admission.finished(), admission.err)
		}
		holder()
		completion.mustGranted(t)
	})
}

// One account's queue does not hold up another account.
func TestGateAccountsAreIndependent(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		g := newAccountGate(200 * time.Millisecond)
		mustAcquire(t, enter(ctx, g, "hot", Completion))
		start(enter(ctx, g, "hot", Admission)).mustWait(t)
		mustAcquire(t, enter(ctx, g, "other", Completion))()
		mustAcquire(t, enter(ctx, g, "other", Admission))()
	})
}

// Waiters that give up while the gate is being handed on must neither keep a grant nor lose one: whatever happens,
// the gate ends free. (The interleaving is the scheduler's, so this is a stress test of an invariant, not of one
// ordering.)
func TestGateSurvivesCancellationsRacingGrants(t *testing.T) {
	g := newAccountGate(time.Hour)
	for round := range 20 {
		done := make(chan struct{})
		go func() { raceRound(t, g, round); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("round %d: the gate is wedged: a grant went to a waiter that had left", round)
		}
		g.mu.Lock()
		left := len(g.accounts)
		g.mu.Unlock()
		if left != 0 {
			t.Fatalf("round %d: %d accounts still held", round, left)
		}
	}
}

// raceRound queues 100 waiters behind a holder, cancels half of them while the holder lets go, and returns when all
// have left.
func raceRound(t *testing.T, g *accountGate, round int) {
	holder, err := g.enter(context.Background(), "a", Completion)
	if err != nil {
		t.Error(err)
		return
	}
	var wg sync.WaitGroup
	cancels := make([]context.CancelFunc, 100)
	for i := range cancels {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel
		class := Admission
		if i%3 == round%3 {
			class = Completion
		}
		wg.Go(func() {
			if release, err := g.enter(ctx, "a", class); err == nil {
				release()
			}
		})
	}
	wg.Go(holder)
	for i := 0; i < len(cancels); i += 2 {
		cancels[i]()
	}
	wg.Wait()
	for _, cancel := range cancels {
		cancel()
	}
}

// Release may run twice on a request's exit paths: the second call must not let a second caller in.
func TestGateReleaseIsIdempotent(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		g := newAccountGate(200 * time.Millisecond)
		first := mustAcquire(t, enter(ctx, g, "a", Completion))
		second := start(enter(ctx, g, "a", Completion))
		second.mustWait(t)
		first()
		second.mustGranted(t)
		third := start(enter(ctx, g, "a", Completion))
		third.mustWait(t)
		first() // already released: it must not hand the gate on again
		third.mustWait(t)
		second.release()
		third.mustGranted(t)
	})
}
