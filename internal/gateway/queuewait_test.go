package gateway

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"axiom/internal/accounts"
	"axiom/internal/platform/pg"
)

// queuedCall admits a call on a handler whose admission is the given function, in its own goroutine, and advances the
// fake clock a second at a time until the call is answered, for at most twice the wait limit. It returns the error and
// the instant the call ended.
func queuedCall(t *testing.T, ctx context.Context, bp *Backpressure, tx func() error, each func(second int)) (time.Time, error) {
	t.Helper()
	h := &Handler{admitTx: func(context.Context, accounts.Caller, accounts.AdmitRequest, func(pg.Tx, accounts.Admission) error) error {
		return tx()
	}}
	h.Limits = bp
	var ended time.Time
	call := start(func() (func(), error) {
		err := h.admit(ctx, &Call{})
		ended = time.Now()
		return nil, err
	})
	for second := range 2 * int(bp.cfg.QueueWait/time.Second) {
		if call.finished() {
			break
		}
		time.Sleep(time.Second)
		if each != nil {
			each(second + 1)
		}
	}
	if !call.finished() {
		t.Fatal("the call was not answered within twice the wait limit")
	}
	return ended, call.err
}

func wantTooManyRequests(t *testing.T, err error) {
	t.Helper()
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Status != http.StatusTooManyRequests || refusal.RetryAfter != 1 {
		t.Fatalf("got %v, want a 429 with Retry-After 1", err)
	}
}

// TestQueueLimitCoversAllTheWaitsOfACall: a call woken for a slot that another call takes queues again, and the limit
// is on all its waits together. Wakes that are not for it arrive every three seconds; the call still gets its 429 when
// the limit since it first queued is spent, not when the last wait is.
func TestQueueLimitCoversAllTheWaitsOfACall(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		bp, err := New(DefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		began := time.Now()
		wakes := func(second int) { // a slot of another model is released every third second
			if second%3 == 0 {
				bp.Queue.Wake()
			}
		}
		ended, err := queuedCall(t, ctx, bp, func() error { return &errWait{after: time.Second} }, wakes) // every endpoint stays full
		wantTooManyRequests(t, err)
		if waited := ended.Sub(began); waited != bp.cfg.QueueWait {
			t.Errorf("the call gave up after %v, want the limit %v", waited, bp.cfg.QueueWait)
		}
		if bp.Queue.queued() != 0 {
			t.Errorf("%d still queued", bp.Queue.queued())
		}
	})
}

// TestQueueLimitHoldsWhenEveryAdmissionSeesARelease: a slot is released elsewhere during each attempt, so the call never
// parks and Wait's fast path answers every time. The call still ends 429 when its time since the first wait is spent,
// with no more than the attempt that was under way.
func TestQueueLimitHoldsWhenEveryAdmissionSeesARelease(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		bp, err := New(DefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		const attempt = time.Second
		var queuedAt time.Time
		ended, err := queuedCall(t, ctx, bp, func() error {
			time.Sleep(attempt)
			bp.Queue.Wake() // a release of another model's slot, found by nobody waiting
			if queuedAt.IsZero() {
				queuedAt = time.Now()
			}
			return &errWait{after: time.Second}
		}, nil)
		wantTooManyRequests(t, err)
		if over := ended.Sub(queuedAt) - bp.cfg.QueueWait; over < 0 || over > attempt {
			t.Errorf("the call ended %v after the limit since it first queued, want within one attempt (%v)", over, attempt)
		}
	})
}
