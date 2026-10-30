package suppliers

import (
	"testing"
	"time"
	"uuid"
)

// capacityView is the live view of an endpoint's credentials, built the way a routing snapshot gets it.
func capacityView(s *Service, id uuid.UUID) *Capacity { return &Capacity{s: s, id: id} }

// TestCapacityFollowsTheCredentialSlots: the view a plan reads says full only while every credential holds a call, and
// it is live: the same view sees a slot taken and given back without the routing snapshot being rebuilt.
func TestCapacityFollowsTheCredentialSlots(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1)
	got := w.routable()
	if len(got) != 1 || got[0].Capacity == nil {
		t.Fatalf("Routable gives %d endpoints, capacity %v", len(got), got)
	}
	view := got[0].Capacity
	if view.Full() {
		t.Fatal("a fresh credential with a free slot reports full")
	}
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !view.Full() {
		t.Error("the only credential holds a call, yet capacity says there is room")
	}
	a.Done()
	if view.Full() {
		t.Error("the call ended, yet capacity still says full")
	}
}

// TestCapacityCountsTheSpentMinute: a credential whose supplier account has spent its rolling minute has no capacity
// either, though its concurrency slot is free; both keys of the account share the window.
func TestCapacityCountsTheSpentMinute(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4)
	second := w.anotherKey(4)
	w.perMinute(w.credential, 1)
	w.perMinute(second, 1)
	a, err := w.svc.Acquire(w.endpoint)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	a.Done() // the concurrency slot is back; the minute is spent
	view := capacityView(w.svc, w.endpoint)
	if !view.Full() {
		t.Error("a spent rolling minute still reports capacity")
	}
	w.clock.advance(61 * time.Second)
	if err := w.svc.Reload(t.Context()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if view.Full() {
		t.Error("the window has passed, yet capacity still says full")
	}
}

// TestCapacityIsTheNodesShare: what a plan sees is this core's share of a finite ceiling, not the credential's
// configured ceiling, and a membership change reaches the same view without a new routing snapshot.
func TestCapacityIsTheNodesShare(t *testing.T) {
	t.Parallel()
	r := newShareCluster()
	a := r.node(t, "a/1", 2, 0)
	syncShares(t, a)
	view := capacityView(a, uuid.Nil())
	if view.Full() {
		t.Fatal("the only core's share has room, yet capacity says full")
	}
	held := takeShares(t, a, 2) // the whole share, which is the configured ceiling while alone
	if !view.Full() {
		t.Error("both slots of the share are taken, yet capacity says there is room")
	}
	doneShares(held)
	b := r.node(t, "b/1", 2, 0)
	syncShares(t, b, a, b) // the ceiling is now split: one slot each
	if view.Full() {
		t.Fatal("the share was cut to one slot, all free, yet capacity says full")
	}
	held = takeShares(t, a, 1)
	if !view.Full() {
		t.Error("the core's one shared slot is taken, yet capacity says there is room")
	}
	doneShares(held)
	if view.Full() {
		t.Error("the shared slot was given back, yet capacity says full")
	}
}
