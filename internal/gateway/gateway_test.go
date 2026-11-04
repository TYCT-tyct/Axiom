package gateway

import (
	"context"
	"errors"
	"testing"
	"time"
)

type ctxKey struct{}

// The cancellation flows from the client to the attempt (when the endpoint passes cancellation on) and never back,
// and the settlement context outlives both.
func TestContextsPropagateCancellation(t *testing.T) {
	t.Parallel()
	run(t, func(t *testing.T, ctx context.Context) {
		client, cancelClient := context.WithCancel(context.WithValue(ctx, ctxKey{}, "id"))
		deadline := time.Now().Add(time.Hour)
		follows, cancelFollows := AttemptContext(client, deadline, true)
		detached, cancelDetached := AttemptContext(client, deadline, false)
		defer cancelFollows()
		defer cancelDetached()

		_, cancelOther := AttemptContext(client, deadline, true)
		cancelOther()
		if client.Err() != nil {
			t.Fatal("cancelling an attempt cancelled the client")
		}
		cancelClient()
		if !errors.Is(follows.Err(), context.Canceled) {
			t.Fatalf("an endpoint that passes cancellation on: attempt err = %v", follows.Err())
		}
		if detached.Err() != nil {
			t.Fatalf("an endpoint that does not: attempt err = %v, want it to run on", detached.Err())
		}
		time.Sleep(time.Hour + time.Second)
		if !errors.Is(detached.Err(), context.DeadlineExceeded) {
			t.Fatalf("the call deadline did not stop the detached attempt: %v", detached.Err())
		}

		settle, cancelSettle := SettleContext(client, 2*time.Second)
		defer cancelSettle()
		if settle.Err() != nil || settle.Value(ctxKey{}) != "id" {
			t.Fatalf("settle context after the client left: err=%v value=%v", settle.Err(), settle.Value(ctxKey{}))
		}
		time.Sleep(3 * time.Second)
		if !errors.Is(settle.Err(), context.DeadlineExceeded) {
			t.Fatalf("settle context has no deadline of its own: %v", settle.Err())
		}
	})
}
