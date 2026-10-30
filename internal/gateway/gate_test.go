package gateway

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// testFunds is a gate and a pool without a database: the runs decide what a batch does.
func testFunds(wait time.Duration) *Funds {
	return &Funds{gate: newBatchGate(wait), pool: newPool(64, 16), budget: time.Second}
}

// TestGateDropsAnAdmissionAlreadyCancelled: a client that is gone before its request reaches the gate gets its
// cancellation back and no transaction is started for it.
func TestGateDropsAnAdmissionAlreadyCancelled(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		bp, err := New(DefaultConfig(), nil)
		if err != nil {
			t.Fatal(err)
		}
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		ran := false
		item := &gateItem{done: make(chan struct{})}
		err = bp.Funds.Admit(canceled, "a", item, func(context.Context, []*gateItem) error { ran = true; return nil })
		if !errors.Is(err, context.Canceled) || ran {
			t.Fatalf("Admit = %v, ran=%v; want context.Canceled and no run", err, ran)
		}
	})
}

// submitAdmission and submitCompletion run a request through the gate with a batch that writes nothing. Tests that
// only need the gate and a connection use them.
func submitAdmission(ctx context.Context, f *Funds, account string) error {
	item := &gateItem{done: make(chan struct{})}
	return f.Admit(ctx, account, item, func(context.Context, []*gateItem) error { return nil })
}

func submitCompletion(ctx context.Context, f *Funds, account string) error {
	item := &gateItem{done: make(chan struct{})}
	return f.Complete(ctx, account, item, func(context.Context, []*gateItem) error { return nil })
}

// submitGate starts one request through the gate, with the run its class would use.
func submitGate(ctx context.Context, f *Funds, account string, class Class, run batchRun) *pending {
	return start(func() (func(), error) {
		item := &gateItem{done: make(chan struct{})}
		var err error
		if class == Completion {
			err = f.Complete(ctx, account, item, run)
		} else {
			err = f.Admit(ctx, account, item, run)
		}
		return func() {}, err
	})
}

// holdGate starts a request whose batch blocks until the returned function is called.
func holdGate(t *testing.T, ctx context.Context, f *Funds, account string, class Class) func() {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	submitGate(ctx, f, account, class, func(context.Context, []*gateItem) error {
		close(started)
		<-release
		return nil
	})
	synctest.Wait()
	select {
	case <-started:
	default:
		t.Fatal("the batch did not start")
	}
	return sync.OnceFunc(func() { close(release) })
}

// A settlement that arrives after an admission still gets the account first.
func TestGateCompletionOvertakesWaitingAdmissions(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		f := testFunds(200 * time.Millisecond)
		release := holdGate(t, ctx, f, "a", Completion)

		var order []string
		admission := submitGate(ctx, f, "a", Admission, func(context.Context, []*gateItem) error {
			order = append(order, "admission")
			return nil
		})
		completion := submitGate(ctx, f, "a", Completion, func(context.Context, []*gateItem) error {
			order = append(order, "completion")
			return nil
		})
		synctest.Wait()
		if admission.finished() || completion.finished() {
			t.Fatalf("a request ran while the gate was held: admission=%v completion=%v", admission.finished(), completion.finished())
		}

		release()
		if !completion.finished() || completion.err != nil {
			t.Fatalf("completion: finished=%v err=%v, want a grant", completion.finished(), completion.err)
		}
		if !admission.finished() || admission.err != nil {
			t.Fatalf("admission: finished=%v err=%v, want a grant", admission.finished(), admission.err)
		}
		if !slices.Equal(order, []string{"completion", "admission"}) {
			t.Fatalf("order %v, want the completion first", order)
		}
	})
}

func TestGateAdmissionGivesUpAfterTheWait(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		f := testFunds(200 * time.Millisecond)
		release := holdGate(t, ctx, f, "a", Completion)
		admission := submitGate(ctx, f, "a", Admission, func(context.Context, []*gateItem) error { return nil })
		time.Sleep(199 * time.Millisecond)
		admission.mustWait(t)
		time.Sleep(time.Millisecond)
		if !admission.finished() || !errors.Is(admission.err, errAccountGate) {
			t.Fatalf("after 200ms: finished=%v err=%v, want errAccountGate", admission.finished(), admission.err)
		}

		// The waiter left no trace: when the holder goes, the account is forgotten.
		release()
		synctest.Wait()
		f.gate.mu.Lock()
		left := len(f.gate.accounts)
		f.gate.mu.Unlock()
		if left != 0 {
			t.Fatalf("%d accounts left in the gate map", left)
		}
	})
}

// A completion carries money already committed to, so it has no wait limit of its own; a client that goes away
// leaves with its own error, not a 429.
func TestGateCompletionKeepsWaitingAndClientCancelIsNotARefusal(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		f := testFunds(200 * time.Millisecond)
		release := holdGate(t, ctx, f, "a", Completion)
		completion := submitGate(ctx, f, "a", Completion, func(context.Context, []*gateItem) error { return nil })
		clientCtx, cancel := context.WithCancel(ctx)
		admission := submitGate(clientCtx, f, "a", Admission, func(context.Context, []*gateItem) error { return nil })

		time.Sleep(time.Minute)
		if !admission.finished() || !errors.Is(admission.err, errAccountGate) {
			t.Fatalf("admission: finished=%v err=%v, want errAccountGate", admission.finished(), admission.err)
		}
		completion.mustWait(t)

		admission = submitGate(clientCtx, f, "a", Admission, func(context.Context, []*gateItem) error { return nil })
		admission.mustWait(t)
		cancel()
		if !admission.finished() || !errors.Is(admission.err, context.Canceled) {
			t.Fatalf("cancelled admission: finished=%v err=%v, want context.Canceled", admission.finished(), admission.err)
		}
		release()
		completion.mustGranted(t)
	})
}

// One account's queue does not hold up another account.
func TestGateAccountsAreIndependent(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		f := testFunds(200 * time.Millisecond)
		release := holdGate(t, ctx, f, "hot", Completion)
		submitGate(ctx, f, "hot", Admission, func(context.Context, []*gateItem) error { return nil }).mustWait(t)
		submitGate(ctx, f, "other", Completion, func(context.Context, []*gateItem) error { return nil }).mustGranted(t)
		submitGate(ctx, f, "other", Admission, func(context.Context, []*gateItem) error { return nil }).mustGranted(t)
		release()
	})
}

// The admissions waiting for one account are written as one batch: the leader takes every waiter, not one per
// transaction, and the run it uses is the first admission's.
func TestGateBatchesWaitingAdmissions(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		f := testFunds(200 * time.Millisecond)
		release := holdGate(t, ctx, f, "a", Completion)
		var sizes []int
		var waiting []*pending
		for range 10 {
			waiting = append(waiting, submitGate(ctx, f, "a", Admission, func(_ context.Context, batch []*gateItem) error {
				sizes = append(sizes, len(batch))
				return nil
			}))
		}
		synctest.Wait()
		for _, p := range waiting {
			p.mustWait(t)
		}
		release()
		time.Sleep(admissionCollect) // the batch's bounded collection ends
		for _, p := range waiting {
			p.mustGranted(t)
		}
		if !slices.Equal(sizes, []int{10}) {
			t.Fatalf("batch sizes %v, want [10]", sizes)
		}
	})
}

// Waiters that give up while the gate is being handed on must neither keep a batch nor lose one: whatever happens, the
// gate ends free. (The interleaving is the scheduler's, so this is a stress test of an invariant, not of one ordering.)
func TestGateSurvivesCancellationsRacingBatches(t *testing.T) {
	f := testFunds(time.Hour)
	for round := range 20 {
		done := make(chan struct{})
		go func() { raceRound(t, f, round); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("round %d: the gate is wedged: a batch never finished", round)
		}
		f.gate.mu.Lock()
		left := len(f.gate.accounts)
		f.gate.mu.Unlock()
		if left != 0 {
			t.Fatalf("round %d: %d accounts still held", round, left)
		}
	}
}

// raceRound queues 100 requests behind a holder, cancels half of them while the holder lets go, and returns when all
// have left.
func raceRound(t *testing.T, f *Funds, round int) {
	ctx := context.Background()
	started, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		item := &gateItem{done: make(chan struct{})}
		_ = f.Complete(ctx, "a", item, func(context.Context, []*gateItem) error {
			close(started)
			<-release
			return nil
		})
	})
	<-started
	cancels := make([]context.CancelFunc, 100)
	for i := range cancels {
		ctx, cancel := context.WithCancel(ctx)
		cancels[i] = cancel
		class := Admission
		if i%3 == round%3 {
			class = Completion
		}
		wg.Go(func() {
			item := &gateItem{done: make(chan struct{})}
			run := func(context.Context, []*gateItem) error { return nil }
			if class == Completion {
				_ = f.Complete(ctx, "a", item, run)
			} else {
				_ = f.Admit(ctx, "a", item, run)
			}
		})
	}
	for i := 0; i < len(cancels); i += 2 {
		cancels[i]()
	}
	close(release)
	wg.Wait()
	for _, cancel := range cancels {
		cancel()
	}
}
