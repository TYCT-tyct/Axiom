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

// TestQueueLimitCoversAllTheWaitsOfACall: a call woken for a slot that another call takes queues again, and the limit
// is on all its waits together. Wakes for a model that is not the one it waits for arrive every three seconds; the
// call still gets its 429 when the limit since it first queued is spent, not when the last wait is.
func TestQueueLimitCoversAllTheWaitsOfACall(t *testing.T) {
	run(t, func(t *testing.T, ctx context.Context) {
		bp, err := New(DefaultConfig())
		if err != nil {
			t.Fatal(err)
		}
		h := &Handler{admitTx: func(context.Context, accounts.Caller, accounts.AdmitRequest, func(pg.Tx, accounts.Admission) error) error {
			return &errWait{after: time.Second} // every candidate endpoint stays full
		}}
		h.Limits = bp
		began := time.Now()
		var ended time.Time
		call := start(func() (func(), error) {
			err := h.admit(ctx, &Call{})
			ended = time.Now()
			return nil, err
		})
		for range 20 { // twice the limit is more than enough
			if call.finished() {
				break
			}
			time.Sleep(3 * time.Second)
			bp.Queue.Wake()
		}
		var refusal *Refusal
		if !call.finished() || !errors.As(call.err, &refusal) || refusal.Status != http.StatusTooManyRequests || refusal.RetryAfter != 1 {
			t.Fatalf("finished=%v err=%v, want a 429 with Retry-After 1", call.finished(), call.err)
		}
		if waited := ended.Sub(began); waited != bp.cfg.QueueWait {
			t.Errorf("the call gave up after %v, want the limit %v", waited, bp.cfg.QueueWait)
		}
		if bp.Queue.queued() != 0 {
			t.Errorf("%d still queued", bp.Queue.queued())
		}
	})
}
