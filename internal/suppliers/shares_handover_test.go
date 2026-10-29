package suppliers

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/kv"
)

func TestUnconfirmedJoinKeepsExistingCoreUsable(t *testing.T) {
	r := newShareCluster()
	a := r.node(t, "a/1", 8, 8)
	syncShares(t, a)
	r.node(t, "b/1", 8, 8)
	// Registration precedes supplier startup: A sees B before B can publish any confirmation.
	syncShares(t, a)
	doneShares(takeShares(t, a, 4))
	fullShare(t, a)
}

func TestUnconfirmedDepartureDoesNotReserveRPM(t *testing.T) {
	r := newShareCluster()
	a := r.node(t, "a/1", 8, 8)
	syncShares(t, a)
	r.node(t, "b/1", 8, 8)
	r.mu.Lock()
	b := r.members["b/1"]
	b.seen = r.now.Add(-kv.AliveFor)
	r.members[b.ID] = b
	r.mu.Unlock()
	syncShares(t, a)
	doneShares(takeShares(t, a, 8))
	fullShare(t, a)
}

func TestExpiredJoinRetainsItsPreviousPartitionForPeers(t *testing.T) {
	r := newShareCluster()
	a := r.node(t, "a/1", 8, 0)
	syncShares(t, a)
	b := r.node(t, "b/1", 8, 0)
	syncShares(t, b, a, b, a)
	held := takeShares(t, a, 4)
	defer doneShares(held)
	r.advance(kv.ShareLease, "a/1", "b/1")
	r.node(t, "c/1", 8, 0)
	// A cannot confirm the smaller target yet, but its previous partition still protects B's capacity.
	syncShares(t, a, b)
	other := takeShares(t, b, 3)
	defer doneShares(other)
	fullShare(t, b)
}

func TestHiddenCredentialsMustDrainBeforeMembershipConfirmation(t *testing.T) {
	for _, rpm := range []bool{false, true} {
		t.Run(map[bool]string{false: "inflight", true: "minute"}[rpm], func(t *testing.T) {
			r := newShareCluster()
			limit := int32(0)
			if rpm {
				limit = 8
			}
			a := r.node(t, "a/1", 8, limit)
			syncShares(t, a)
			held := takeShares(t, a, 8)
			defer doneShares(held)
			if rpm {
				doneShares(held)
			}
			b := r.node(t, "b/1", 8, limit)
			syncShares(t, b)
			visible := a.snap.Load()
			hidden := cloneSnapshot(visible)
			hidden.endpoints = map[EndpointID]*endpoint{}
			a.snap.Store(hidden)
			syncShares(t, a, b)
			fullShare(t, b)
			// A temporarily had no routable rows; returning the credential must not create a fresh global partition.
			a.snap.Store(visible)
			syncShares(t, a, b)
			fullShare(t, b)
			if rpm {
				r.advance(61*time.Second, "a/1", "b/1")
			} else {
				doneShares(held[4:])
			}
			syncShares(t, a, b, a)
			other := takeShares(t, b, 4)
			defer doneShares(other)
			fullShare(t, b)
		})
	}
}

func TestAcquireRetriesAReplacedSnapshot(t *testing.T) {
	r := newShareCluster()
	a := r.node(t, "a/1", 2, 0)
	syncShares(t, a)
	started, resume := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(resume) })
	defer release()
	var pause atomic.Bool
	pause.Store(true)
	a.now = func() time.Time {
		if pause.CompareAndSwap(true, false) {
			close(started)
			<-resume
		}
		return r.clock()
	}
	type acquired struct {
		attempt *Attempt
		err     error
	}
	result := make(chan acquired, 1)
	go func() { attempt, err := a.Acquire(uuid.Nil()); result <- acquired{attempt, err} }()
	<-started
	syncShares(t, a)
	release()
	got := <-result
	if got.err != nil {
		t.Fatalf("available credential rejected after snapshot refresh: %v", got.err)
	}
	got.attempt.Done()
}

type changingShares struct {
	ShareRegistry
	changed bool
}

func (r *changingShares) ConfirmShares(ctx context.Context, view []string) error {
	if !r.changed && view != nil {
		r.changed = true
		return kv.ErrShareMembershipChanged
	}
	return r.ShareRegistry.ConfirmShares(ctx, view)
}

func TestMembershipRaceDefersConfirmationWithoutFailingStartup(t *testing.T) {
	r := newShareCluster()
	a := r.node(t, "a/1", 2, 0)
	a.cfg.Nodes = &changingShares{ShareRegistry: a.cfg.Nodes}
	syncShares(t, a)
	fullShare(t, a)
	syncShares(t, a)
	held := takeShares(t, a, 2)
	defer doneShares(held)
	fullShare(t, a)
}
