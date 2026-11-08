package gateway

import (
	"context"
	"errors"
	"testing"

	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// TestRebuildGivesUpWithItsContext: a write's rebuild waits for the one in progress, which may have read the sources
// before the write, but gives up when its context ends, and so does a rebuild stuck on a source; the next rebuild
// publishes once the source frees.
func TestRebuildGivesUpWithItsContext(t *testing.T) {
	t.Parallel()
	f := newRouteFixture(t)
	offered := []suppliers.Routable{routable(f.served, protocol.Messages)}
	f.build(nil)
	entered := make(chan struct{})
	stuck := true
	f.deps.Endpoints = func(ctx context.Context) ([]suppliers.Routable, error) {
		if !stuck {
			return offered, nil
		}
		close(entered)
		<-ctx.Done() // a source read that holds on, as one waiting on a table lock does, until its context ends
		return nil, ctx.Err()
	}
	first, cancelFirst := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- f.deps.Rebuild(first) }()
	<-entered

	waiting, cancelWaiting := context.WithCancel(t.Context())
	cancelWaiting()
	if err := f.deps.Rebuild(waiting); !errors.Is(err, context.Canceled) {
		t.Fatalf("a rebuild waiting for the stuck one = %v, want it to give up with its context", err)
	}
	cancelFirst()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("the stuck rebuild = %v, want it to give up with its context", err)
	}

	stuck = false // only this goroutine runs a rebuild now
	if err := f.deps.Rebuild(t.Context()); err != nil {
		t.Fatalf("the rebuild after the source freed: %v", err)
	}
	if got := f.planned(call(f.served)); len(got) != 1 || got[0] != offered[0].ID {
		t.Fatalf("after the source freed the route is %v, want the endpoint %s", got, offered[0].ID)
	}
}
