package suppliers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/kv"
)

type shareMember struct {
	kv.ShareNode
	seen time.Time
}
type shareCluster struct {
	mu      sync.Mutex
	now     time.Time
	members map[string]shareMember
	fail    bool
}
type fakeShares struct {
	cluster *shareCluster
	id      string
}

func (r fakeShares) ShareID() string { return r.id }
func (r fakeShares) ShareView(context.Context) ([]kv.ShareNode, error) {
	r.cluster.mu.Lock()
	defer r.cluster.mu.Unlock()
	if r.cluster.fail {
		return nil, errors.New("registry unavailable")
	}
	return r.cluster.live(), nil
}

func (r *shareCluster) live() []kv.ShareNode {
	var nodes []kv.ShareNode
	for _, m := range r.members {
		if r.now.Sub(m.seen) < kv.AliveFor+61*time.Second {
			nodes = append(nodes, kv.ShareNode{ID: m.ID, View: slices.Clone(m.View), Remaining: kv.AliveFor - r.now.Sub(m.seen)})
		}
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	return nodes
}

func (r fakeShares) ConfirmShares(_ context.Context, view []string) error {
	r.cluster.mu.Lock()
	defer r.cluster.mu.Unlock()
	if r.cluster.fail {
		return errors.New("registry unavailable")
	}
	nodes := r.cluster.live()
	var ids []string
	for _, n := range nodes {
		if n.Remaining > 0 {
			ids = append(ids, n.ID)
		}
	}
	if view != nil && !slices.Equal(view, ids) {
		return errors.New("membership changed")
	}
	m := r.cluster.members[r.id]
	m.View = slices.Clone(view)
	r.cluster.members[r.id] = m
	return nil
}

func newShareCluster() *shareCluster {
	return &shareCluster{now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC), members: map[string]shareMember{}}
}
func (r *shareCluster) clock() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now }
func (r *shareCluster) advance(d time.Duration, beating ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = r.now.Add(d)
	for _, id := range beating {
		m := r.members[id]
		m.seen = r.now
		r.members[id] = m
	}
}

func (r *shareCluster) node(t testing.TB, id string, max, rpm int32) *Service {
	t.Helper()
	r.mu.Lock()
	member := shareMember{
		seen: r.now,
		ID:   id,
	}
	r.members[id] = member
	r.mu.Unlock()
	s, err := New(Config{Clock: r.clock, Nodes: fakeShares{r, id}})
	if err != nil {
		t.Fatal(err)
	}
	credentialID, supplierID := uuid.NewV7(), uuid.NewV7()
	c := &credential{id: credentialID, max: max, configuredMax: max, perMinute: rpm, configuredMinute: rpm, rt: s.runtimeOf(credentialID, supplierID)}
	s.snap.Store(&snapshot{endpoints: map[EndpointID]*endpoint{uuid.Nil(): {creds: []*credential{c}}}})
	return s
}

func syncShares(t testing.TB, nodes ...*Service) {
	t.Helper()
	for _, s := range nodes {
		if err := s.SyncShares(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func takeShares(t testing.TB, s *Service, count int) []*Attempt {
	t.Helper()
	var calls []*Attempt
	for range count {
		a, err := s.Acquire(uuid.Nil())
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		calls = append(calls, a)
	}
	return calls
}

func fullShare(t testing.TB, s *Service) {
	t.Helper()
	a, err := s.Acquire(uuid.Nil())
	if a != nil {
		a.Done()
	}
	if !errors.Is(err, ErrNoSlot) {
		t.Fatalf("expected no slot, got %v", err)
	}
}

func doneShares(calls []*Attempt) {
	for _, a := range calls {
		a.Done()
	}
}

func TestSharesDrainBeforeJoiningCoresAdmit(t *testing.T) {
	for _, ceiling := range []int32{1, 2, 7} {
		t.Run(fmt.Sprint(ceiling), func(t *testing.T) {
			r := newShareCluster()
			a := r.node(t, "a/1", ceiling, 0)
			syncShares(t, a)
			held := map[*Service][]*Attempt{a: takeShares(t, a, int(ceiling))}
			t.Cleanup(func() {
				for _, calls := range held {
					doneShares(calls)
				}
			})
			check := func() { checkHeldShares(t, held, ceiling) }
			b := r.node(t, "b/1", ceiling, 0)
			syncShares(t, b, a, b)
			fullShare(t, b)
			check()
			// A still owns its old calls; reducing its ceiling must neither cut them nor acknowledge too soon.
			fullShare(t, a)
			target := share(ceiling, []string{"a/1", "b/1"}, "a/1")
			doneShares(held[a][target:])
			held[a] = held[a][:target]
			syncShares(t, a, b, a)
			held[b] = takeShares(t, b, int(ceiling-target))
			fullShare(t, a)
			fullShare(t, b)
			check()
			// Join before both existing ranks, exercising a change in remainder ownership as well as N.
			c := r.node(t, "0/1", ceiling, 0)
			syncShares(t, c, a, b, c)
			fullShare(t, c)
			check()
			ids := []string{"0/1", "a/1", "b/1"}
			for _, s := range []*Service{a, b} {
				n := int(share(ceiling, ids, s.cfg.Nodes.ShareID()))
				doneShares(held[s][n:])
				held[s] = held[s][:n]
			}
			syncShares(t, a, b, c, a, b, c)
			held[c] = takeShares(t, c, int(share(ceiling, ids, "0/1")))
			check()
			for _, s := range []*Service{a, b, c} {
				fullShare(t, s)
			}
			// A graceful departure releases its calls before its heartbeat row disappears.
			doneShares(held[b])
			delete(held, b)
			r.mu.Lock()
			delete(r.members, "b/1")
			r.mu.Unlock()
			syncShares(t, a, c, a, c)
			for _, s := range []*Service{a, c} {
				n := int(share(ceiling, []string{"0/1", "a/1"}, s.cfg.Nodes.ShareID()))
				held[s] = append(held[s], takeShares(t, s, n-len(held[s]))...)
				fullShare(t, s)
				check()
			}
		})
	}
}

func TestSharesWaitForTheAccountsOldMinute(t *testing.T) {
	r := newShareCluster()
	a := r.node(t, "a/1", 5, 5)
	syncShares(t, a)
	doneShares(takeShares(t, a, 5))
	b := r.node(t, "b/1", 5, 5)
	syncShares(t, b, a, b)
	fullShare(t, a)
	fullShare(t, b)
	// Releasing concurrency did not erase RPM, so B remains closed until the old window leaves.
	r.advance(61*time.Second, "a/1", "b/1")
	syncShares(t, a, b, a, b, a)
	doneShares(takeShares(t, a, 3))
	doneShares(takeShares(t, b, 2))
	fullShare(t, a)
	fullShare(t, b)
}

func TestShareLeaseFencesAStoppedSynchronizerAndRejoins(t *testing.T) {
	r := newShareCluster()
	a := r.node(t, "a/1", 6, 0)
	syncShares(t, a)
	b := r.node(t, "b/1", 6, 0)
	syncShares(t, b, a, b, a)
	r.advance(kv.ShareLease, "a/1")
	fullShare(t, b)
	syncShares(t, a)
	r.advance(kv.AliveFor-kv.ShareLease+time.Second, "a/1")
	syncShares(t, a, a)
	calls := takeShares(t, a, 6)
	fullShare(t, a)
	// B's heartbeat returns. Clearing its expired confirmation makes it a joining core, even with the same identity.
	r.advance(0, "b/1")
	syncShares(t, b, a, b)
	fullShare(t, b)
	doneShares(calls[3:])
	calls = calls[:3]
	syncShares(t, a, b, a)
	other := takeShares(t, b, 3)
	fullShare(t, a)
	fullShare(t, b)
	doneShares(calls)
	doneShares(other)
}

func TestShareFailureCannotRenewAdmission(t *testing.T) {
	r := newShareCluster()
	a := r.node(t, "a/1", 2, 0)
	syncShares(t, a)
	r.mu.Lock()
	r.fail = true
	r.mu.Unlock()
	if err := a.SyncShares(t.Context()); err == nil {
		t.Fatal("registry failure was ignored")
	}
	doneShares(takeShares(t, a, 2))
	r.advance(kv.ShareLease)
	fullShare(t, a)
}

func TestAnOldSnapshotCannotSpendAfterConfirmation(t *testing.T) {
	r := newShareCluster()
	a := r.node(t, "a/1", 2, 2)
	syncShares(t, a)
	old := a.snap.Load()
	c := old.endpoints[uuid.Nil()].creds[0]
	b := r.node(t, "b/1", 2, 2)
	syncShares(t, b, a, b, a)
	// Model an Acquire suspended after reading its old snapshot and before taking the account lock.
	c.rt.inflight.Add(1)
	if c.rt.requests.takeCurrent(c.perMinute, a, old) {
		t.Fatal("old snapshot admitted after handover")
	}
	c.rt.inflight.Add(-1)
	doneShares(takeShares(t, a, 1))
	doneShares(takeShares(t, b, 1))
	fullShare(t, a)
	fullShare(t, b)
}

func TestConcurrentCallsRespectStaticShares(t *testing.T) {
	r := newShareCluster()
	var nodes []*Service
	for _, id := range []string{"a/1", "b/1", "c/1"} {
		nodes = append(nodes, r.node(t, id, 7, 7))
	}
	for range 3 {
		syncShares(t, nodes...)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var held []*Attempt
	for _, s := range nodes {
		for range 24 {
			wg.Go(func() {
				a, err := s.Acquire(uuid.Nil())
				if err == nil {
					mu.Lock()
					held = append(held, a)
					mu.Unlock()
				} else if !errors.Is(err, ErrNoSlot) {
					t.Error(err)
				}
			})
		}
	}
	wg.Wait()
	defer doneShares(held)
	if len(held) != 7 {
		t.Fatalf("admitted %d calls, want 7", len(held))
	}
}

func BenchmarkAcquire(b *testing.B) {
	for _, shared := range []bool{false, true} {
		b.Run(fmt.Sprint(shared), func(b *testing.B) {
			r := newShareCluster()
			s := r.node(b, "a/1", 4, 0)
			syncShares(b, s)
			if !shared {
				s.cfg.Nodes = nil
			}
			b.ReportAllocs()
			for b.Loop() {
				a, err := s.Acquire(uuid.Nil())
				if err != nil {
					b.Fatal(err)
				}
				a.Done()
			}
		})
	}
}

func TestDepartedCoresReserveTheirRPMUntilTheWindowEnds(t *testing.T) {
	r := newShareCluster()
	a := r.node(t, "a/1", 6, 6)
	syncShares(t, a)
	b := r.node(t, "b/1", 6, 6)
	syncShares(t, b, a, b, a)
	doneShares(takeShares(t, b, 3))
	r.advance(31*time.Second, "a/1")
	syncShares(t, a, a)
	doneShares(takeShares(t, a, 3))
	fullShare(t, a)
	// Even a fresh core must not reuse the dead core's previous half of the rolling minute.
	c := r.node(t, "c/1", 6, 6)
	syncShares(t, c, a, c, a)
	fullShare(t, c)
	r.advance(61*time.Second, "a/1", "c/1")
	syncShares(t, a, c, a, c)
	doneShares(takeShares(t, a, 3))
	doneShares(takeShares(t, c, 3))
	fullShare(t, a)
	fullShare(t, c)
}

func checkHeldShares(t *testing.T, held map[*Service][]*Attempt, ceiling int32) {
	t.Helper()
	total := 0
	for _, calls := range held {
		total += len(calls)
	}
	if total > int(ceiling) {
		t.Fatalf("%d held > ceiling %d", total, ceiling)
	}
}

func TestReadingAnOldHeartbeatDoesNotExtendItsAdmissionLease(t *testing.T) {
	r := newShareCluster()
	a := r.node(t, "a/1", 1, 0)
	syncShares(t, a)
	r.advance(20 * time.Second)
	syncShares(t, a)
	doneShares(takeShares(t, a, 1))
	r.advance(5 * time.Second)
	fullShare(t, a)
}

func TestJoiningCoresWaitForLegacyProcesses(t *testing.T) {
	r := newShareCluster()
	r.mu.Lock()
	legacy := shareMember{
		seen: r.now,
		ID:   "old/legacy",
	}
	r.members[legacy.ID] = legacy
	r.mu.Unlock()
	a := r.node(t, "a/1", 3, 3)
	syncShares(t, a)
	fullShare(t, a)
	// A legacy binary cannot acknowledge the new partition. Its old minute survives the process as well.
	r.advance(31*time.Second, "a/1")
	syncShares(t, a, a)
	fullShare(t, a)
	r.advance(61*time.Second, "a/1")
	syncShares(t, a)
	doneShares(takeShares(t, a, 3))
	fullShare(t, a)
}

func TestRPMCeilingsSmallerThanCoreCountAreNotUnlimited(t *testing.T) {
	for _, ceiling := range []int32{1, 2, 7} {
		t.Run(fmt.Sprint(ceiling), func(t *testing.T) {
			r := newShareCluster()
			ids := []string{"a/1", "b/1", "c/1"}
			var nodes []*Service
			for _, id := range ids {
				nodes = append(nodes, r.node(t, id, 9, ceiling))
			}
			for range 3 {
				syncShares(t, nodes...)
			}
			for i, s := range nodes {
				expected := int(ceiling) / len(nodes)
				if i < int(ceiling)%len(nodes) {
					expected++
				}
				doneShares(takeShares(t, s, expected))
				fullShare(t, s)
			}
		})
	}
}

func TestReplacementWaitsForThePreviousAdmissionLease(t *testing.T) {
	r := newShareCluster()
	old := r.node(t, "a/1", 2, 0)
	syncShares(t, old)
	calls := takeShares(t, old, 2)
	replacement := r.node(t, "a/2", 2, 0)
	syncShares(t, replacement)
	fullShare(t, replacement)
	r.advance(31*time.Second, "a/2")
	fullShare(t, old)
	doneShares(calls)
	syncShares(t, replacement, replacement)
	newCalls := takeShares(t, replacement, 2)
	defer doneShares(newCalls)
	fullShare(t, replacement)
}
