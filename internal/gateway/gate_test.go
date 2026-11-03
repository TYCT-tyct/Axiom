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
	t.Parallel()
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

// holdGate starts a completion whose batch blocks until the returned function is called.
func holdGate(t *testing.T, ctx context.Context, f *Funds, account string) func() {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	submitGate(ctx, f, account, Completion, func(context.Context, []*gateItem) error {
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
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		f := testFunds(200 * time.Millisecond)
		release := holdGate(t, ctx, f, "a")

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
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		f := testFunds(200 * time.Millisecond)
		release := holdGate(t, ctx, f, "a")
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
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		f := testFunds(200 * time.Millisecond)
		release := holdGate(t, ctx, f, "a")
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
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		f := testFunds(200 * time.Millisecond)
		release := holdGate(t, ctx, f, "hot")
		submitGate(ctx, f, "hot", Admission, func(context.Context, []*gateItem) error { return nil }).mustWait(t)
		submitGate(ctx, f, "other", Completion, func(context.Context, []*gateItem) error { return nil }).mustGranted(t)
		submitGate(ctx, f, "other", Admission, func(context.Context, []*gateItem) error { return nil }).mustGranted(t)
		release()
	})
}

// The admissions waiting for one account are written as one batch: the leader takes every waiter, not one per
// transaction, and the run it uses is the first admission's.
func TestGateBatchesWaitingAdmissions(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		f := testFunds(200 * time.Millisecond)
		release := holdGate(t, ctx, f, "a")
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

// TestGateLeaderReturnsWithItsBatch: the first request of an idle account starts the leader but does not stay in it.
// Once its own batch ends it returns, even when the next batch is already running and blocked; otherwise a busy
// account could hold an admission before its upstream call, an extension's landing, or its cancel for as long as the
// queue keeps arriving.
func TestGateLeaderReturnsWithItsBatch(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		f := testFunds(time.Second)
		firstStarted, firstRelease := make(chan struct{}), make(chan struct{})
		first := submitGate(ctx, f, "a", Admission, func(context.Context, []*gateItem) error {
			close(firstStarted)
			<-firstRelease
			return nil
		})
		synctest.Wait()
		select {
		case <-firstStarted:
		default:
			t.Fatal("the first batch did not start")
		}
		secondStarted, secondRelease := make(chan struct{}), make(chan struct{})
		second := submitGate(ctx, f, "a", Admission, func(context.Context, []*gateItem) error {
			close(secondStarted)
			<-secondRelease
			return nil
		})
		synctest.Wait()
		if first.finished() {
			t.Fatal("the first batch ended before its release")
		}
		close(firstRelease)
		synctest.Wait()
		select {
		case <-secondStarted:
		default:
			t.Fatal("the second batch did not start")
		}
		if !first.finished() {
			t.Fatal("the leader is still serving the next batch instead of returning its own result")
		}
		if second.finished() {
			t.Fatal("the second batch ended before its release")
		}
		close(secondRelease)
		second.mustGranted(t)
	})
}

// TestGateFailedCompletionReturnsWhileTheNextBatchRuns: the completion that carries a failed extension returns with
// its error as soon as its own batch ends, even while the next batch is blocked. The caller then sends the error to
// the meter's landing channel and cancels the upstream, so the wait for later work cannot stretch ExtendWait.
func TestGateFailedCompletionReturnsWhileTheNextBatchRuns(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		f := testFunds(time.Second)
		boom := errors.New("extension refused")
		firstStarted, firstRelease := make(chan struct{}), make(chan struct{})
		first := submitGate(ctx, f, "a", Completion, func(context.Context, []*gateItem) error {
			close(firstStarted)
			<-firstRelease
			return boom
		})
		synctest.Wait()
		select {
		case <-firstStarted:
		default:
			t.Fatal("the failed completion's batch did not start")
		}
		secondStarted, secondRelease := make(chan struct{}), make(chan struct{})
		second := submitGate(ctx, f, "a", Admission, func(context.Context, []*gateItem) error {
			close(secondStarted)
			<-secondRelease
			return nil
		})
		synctest.Wait()
		close(firstRelease)
		synctest.Wait()
		select {
		case <-secondStarted:
		default:
			t.Fatal("the next batch did not start")
		}
		if !first.finished() || !errors.Is(first.err, boom) {
			t.Fatalf("the failed completion has not returned: finished=%v err=%v", first.finished(), first.err)
		}
		if second.finished() {
			t.Fatal("the next batch ended before its release")
		}
		close(secondRelease)
		second.mustGranted(t)
	})
}

// Waiters that give up while the gate is being handed on must neither keep a batch nor lose one: whatever happens, the
// gate ends free. (The interleaving is the scheduler's, so this is a stress test of an invariant, not of one ordering.)
// The bubble turns a wedged gate into a deadlock report, and synctest.Wait lets the leader finish: a request returns
// when its batch ends, and only then does the leader find the queue empty and free the account (a check made right
// after the last request returned failed the train on 2026-11-02, run 41467253974).
func TestGateSurvivesCancellationsRacingBatches(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		f := testFunds(time.Hour)
		for round := range 20 {
			raceRound(t, f, round)
			synctest.Wait()
			f.gate.mu.Lock()
			left := len(f.gate.accounts)
			f.gate.mu.Unlock()
			if left != 0 {
				t.Fatalf("round %d: %d accounts still held", round, left)
			}
		}
	})
}

// raceRound queues 100 requests behind a holder, cancels half of them while the holder lets go, and returns when all
// have left. Every request is granted or cancelled: the admission wait is longer than the test.
func raceRound(t *testing.T, f *Funds, round int) {
	ctx := context.Background()
	started, release := make(chan struct{}), make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		item := &gateItem{done: make(chan struct{})}
		if err := f.Complete(ctx, "a", item, func(context.Context, []*gateItem) error {
			close(started)
			<-release
			return nil
		}); err != nil {
			t.Errorf("round %d: the holder: %v", round, err)
		}
	})
	<-started
	cancels := make([]context.CancelFunc, 100)
	for i := range cancels {
		ctx, cancel := context.WithCancel(ctx)
		cancels[i] = cancel
		submit := f.Admit
		if i%3 == round%3 {
			submit = f.Complete
		}
		wg.Go(func() {
			item := &gateItem{done: make(chan struct{})}
			err := submit(ctx, "a", item, func(context.Context, []*gateItem) error { return nil })
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("round %d: %v, want granted or cancelled", round, err)
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
