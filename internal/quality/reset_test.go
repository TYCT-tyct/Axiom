package quality

import (
	"errors"
	"testing"
	"time"

	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// TestResetHealthClosesTheBreaker: a reset of an offline endpoint closes its breaker in the store and, once adopted, on
// this node at once, with the slow start, the incident's signals resolved and a reset event; another node adopts it
// from the store when a transition it built on the old state loses, and does not probe; a rolled-back reset changes
// nothing, and a reset of a closed breaker keeps it closed.
func TestResetHealthClosesTheBreaker(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Enforce)
	id := addProbeEndpoint(f)
	other, otherLoop := offlineOnTwoNodes(t, m, l, f, now, id)
	reset := func(commit bool) HealthReset {
		t.Helper()
		return resetIn(t, m, l, id, *now, commit)
	}

	reset(false)
	if at, _ := m.Live(id, 1).Routing(*now); at.IsZero() || resetEvents(t, l) != 0 {
		t.Fatal("a rolled-back reset changed the breaker")
	}

	m.AdoptReset(reset(true))
	checkResetHere(t, m, l, id, *now)

	// Another node still holds the open breaker; a transition it queued on that state must lose to the reset.
	queueStaleBackoff(t, other, otherLoop, id)
	if at, _ := other.Live(id, 1).Routing(*now); !at.IsZero() || storedOpened(t, l, id) {
		t.Fatal("a transition built on the old breaker overwrote the reset")
	}
	*now = now.Add(10 * time.Minute)
	tick(t, otherLoop)
	if otherLoop.active != 0 {
		t.Fatal("another node probed an endpoint a person had reset")
	}

	m.AdoptReset(reset(true))
	if at, _ := m.Live(id, 1).Routing(*now); !at.IsZero() || resetEvents(t, l) != 2 {
		t.Fatal("a reset of a closed breaker opened it or recorded no event")
	}
}

// TestResetHealthVoidsATripNotYetStored: a breaker that tripped in memory, before its first row or its open state was
// stored, is reset too, on the node that adopts the reset and on one that only learns it from the store.
func TestResetHealthVoidsATripNotYetStored(t *testing.T) {
	for _, tc := range []struct {
		name          string
		stored, adopt bool
	}{
		{"no row, adopted here", false, true},
		{"no row, learnt from the store", false, false},
		{"a closed row, adopted here", true, true},
		{"a closed row, learnt from the store", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) { resetUnstoredTrip(t, tc.stored, tc.adopt) })
	}
}

// resetUnstoredTrip trips an endpoint in memory only, over no row or a stored closed one, resets it, adopts the reset
// here or not, and checks the trip is not written over the reset.
func resetUnstoredTrip(t *testing.T, stored, adopt bool) {
	t.Helper()
	m, l, f, now := fixtureLoop(t, Enforce)
	id := addProbeEndpoint(f)
	if stored {
		m.Live(id, 1)
		queueStaleBackoff(t, m, l, id) // a stored, closed row
	}
	removeEndpoint(t, m, id, *now) // the trip is queued, not written
	if at, _ := m.Live(id, 1).Routing(*now); at.IsZero() || storedOpened(t, l, id) {
		t.Fatal("the fixture did not trip in memory only")
	}
	r := resetIn(t, m, l, id, *now, true)
	if adopt {
		m.AdoptReset(r)
	}
	if err := l.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if at, _ := m.Live(id, 1).Routing(*now); !at.IsZero() || storedOpened(t, l, id) {
		t.Fatal("the trip from before the reset was written over it")
	}
}

// TestResetHealthKeepsATripAfterIt: a trip made after the reset survives the two late arrivals around it: a write
// built on the old state that finishes after the reset was adopted, and a reset adopted after the node has already read
// it from the store and tripped again.
func TestResetHealthKeepsATripAfterIt(t *testing.T) {
	t.Run("a write in flight across the reset", func(t *testing.T) {
		m, l, f, now := fixtureLoop(t, Enforce)
		id := addProbeEndpoint(f)
		removeEndpoint(t, m, id, *now)
		tick(t, l) // the breaker is stored open
		old := queuedBackoff(t, m, id)
		if err := l.writeWith(t.Context(), old, func() {
			// The change has passed the staleness check; the reset commits and is adopted, and the endpoint trips anew.
			m.AdoptReset(resetIn(t, m, l, id, *now, true))
			*now = now.Add(2 * time.Second)
			removeEndpoint(t, m, id, *now)
		}); err != nil {
			t.Fatal(err)
		}
		checkTrippedAfterReset(t, m, l, id, *now)
	})
	t.Run("a reset adopted after the store had it", func(t *testing.T) {
		m, l, f, now := fixtureLoop(t, Enforce)
		id := addProbeEndpoint(f)
		removeEndpoint(t, m, id, *now)
		tick(t, l)
		r := resetIn(t, m, l, id, *now, true)
		queueStaleBackoff(t, m, l, id) // loses, and this node reads the reset from the store
		*now = now.Add(2 * time.Second)
		removeEndpoint(t, m, id, *now)
		m.AdoptReset(r)
		checkTrippedAfterReset(t, m, l, id, *now)
	})
}

// checkTrippedAfterReset checks that the trip after the reset is held in memory and, once flushed, stored.
func checkTrippedAfterReset(t *testing.T, m *Monitor, l *Loop, id suppliers.EndpointID, now time.Time) {
	t.Helper()
	if at, _ := m.Live(id, 1).Routing(now); at.IsZero() {
		t.Fatal("the trip after the reset was dropped in memory")
	}
	if err := l.flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !storedOpened(t, l, id) {
		t.Fatal("the trip after the reset was not stored")
	}
}

// offlineOnTwoNodes trips the endpoint on m, fails its re-check so it is offline, and starts a second node that loads
// that state; the signals so far are consumed.
func offlineOnTwoNodes(t *testing.T, m *Monitor, l *Loop, f *probeFixture, now *time.Time, id suppliers.EndpointID) (*Monitor, *Loop) {
	t.Helper()
	removeEndpoint(t, m, id, *now)
	tick(t, l)
	*now = now.Add(30 * time.Second)
	tick(t, l)
	f.mu.Lock()
	f.result = suppliers.TestResult{Failure: &suppliers.Failure{Scope: suppliers.ScopeEndpoint, Cause: suppliers.UpstreamError{}}}
	f.mu.Unlock()
	finishProbe(t, l, f)
	other, otherLoop, err := Start(t.Context(), Config{DB: l.db, Scope: l.scope, Mode: Enforce, Source: f, Pauses: f, Now: l.now, Rand: l.rand})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Live(id, 1).Offline(*now) || !other.Live(id, 1).Offline(*now) {
		t.Fatal("the failed re-check did not take the endpoint offline on both nodes")
	}
	consumeHealthSignals(t, l)
	return other, otherLoop
}

var errRolledBack = errors.New("rolled back")

// resetIn runs m's ResetHealth in a transaction that commits or rolls back.
func resetIn(t *testing.T, m *Monitor, l *Loop, id suppliers.EndpointID, at time.Time, commit bool) (r HealthReset) {
	t.Helper()
	err := l.db.Tx(t.Context(), l.scope, func(tx pg.Tx) error {
		var err error
		if r, err = m.ResetHealth(t.Context(), tx, id, at); err != nil || commit {
			return err
		}
		return errRolledBack
	})
	if commit && err != nil {
		t.Fatal(err)
	}
	return r
}

// queuedBackoff has m queue a backoff change built on the breaker it holds, and takes it off the queue.
func queuedBackoff(t *testing.T, m *Monitor, id suppliers.EndpointID) change {
	t.Helper()
	e := m.Live(id, 1)
	e.mu.Lock()
	stale := e.record
	stale.Backoff = maxProbeBackoff
	queued := m.transition(e, stale, "", false)
	e.mu.Unlock()
	if !queued {
		t.Fatal("the change was not queued")
	}
	return <-m.changes
}

// queueStaleBackoff has m queue and write a backoff change built on the breaker it holds.
func queueStaleBackoff(t *testing.T, m *Monitor, l *Loop, id suppliers.EndpointID) {
	t.Helper()
	if err := l.write(t.Context(), queuedBackoff(t, m, id)); err != nil {
		t.Fatal(err)
	}
}

// checkResetHere checks the node that adopted a reset: closed with the slow start, one reset event, and the offline
// alert resolved.
func checkResetHere(t *testing.T, m *Monitor, l *Loop, id suppliers.EndpointID, now time.Time) {
	t.Helper()
	e := m.Live(id, 1)
	if at, limit := e.Routing(now); !at.IsZero() || limit != 1 || e.Offline(now) {
		t.Fatalf("after the reset: opened %v, limit %d, offline %v", at, limit, e.Offline(now))
	}
	if resetEvents(t, l) != 1 {
		t.Fatal("the reset recorded no event")
	}
	resolved := map[string]bool{}
	for _, s := range consumeHealthSignals(t, l) {
		resolved[s.Kind] = resolved[s.Kind] || s.Resolved
	}
	if !resolved["endpoint_still_down"] {
		t.Fatalf("the reset left the offline alert open: %v", resolved)
	}
}

func resetEvents(t *testing.T, l *Loop) (n int) {
	t.Helper()
	if err := l.db.ReadTx(t.Context(), l.scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM quality.health_events WHERE kind='reset'`).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// storedOpened reports whether the stored breaker is open; no row is closed.
func storedOpened(t *testing.T, l *Loop, id suppliers.EndpointID) (open bool) {
	t.Helper()
	if err := l.db.ReadTx(t.Context(), l.scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*)>0 FROM quality.endpoint_health WHERE endpoint_id=$1 AND opened_at>0`, id).Scan(&open)
	}); err != nil {
		t.Fatal(err)
	}
	return open
}
