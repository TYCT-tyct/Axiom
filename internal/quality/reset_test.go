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
// from the store when a transition it built on the old state loses, and does not probe; a rolled-back reset and a
// reset of a closed breaker change nothing.
func TestResetHealthClosesTheBreaker(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Enforce)
	id := addProbeEndpoint(f)
	other, otherLoop := offlineOnTwoNodes(t, m, l, f, now, id)
	reset := func(commit bool) HealthReset {
		t.Helper()
		return resetIn(t, l, id, *now, commit)
	}

	reset(false)
	if at, _ := m.Live(id, 1).Routing(*now); at.IsZero() || resetEvents(t, l) != 0 {
		t.Fatal("a rolled-back reset changed the breaker")
	}

	m.AdoptReset(reset(true))
	checkResetHere(t, m, l, id, *now)

	// Another node still holds the open breaker; a transition it queued on that state must lose to the reset.
	queueStaleBackoff(t, other, otherLoop, id)
	if at, _ := other.Live(id, 1).Routing(*now); !at.IsZero() || storedBackoff(t, l, id) != 0 {
		t.Fatal("a transition built on the old breaker overwrote the reset")
	}
	*now = now.Add(10 * time.Minute)
	tick(t, otherLoop)
	if otherLoop.active != 0 {
		t.Fatal("another node probed an endpoint a person had reset")
	}

	if r := reset(true); r.done || resetEvents(t, l) != 1 {
		t.Fatal("a reset of a closed breaker changed it")
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

// resetIn runs ResetHealth in a transaction that commits or rolls back.
func resetIn(t *testing.T, l *Loop, id suppliers.EndpointID, at time.Time, commit bool) (r HealthReset) {
	t.Helper()
	err := l.db.Tx(t.Context(), l.scope, func(tx pg.Tx) error {
		var err error
		if r, err = ResetHealth(t.Context(), tx, id, at); err != nil || commit {
			return err
		}
		return errRolledBack
	})
	if commit && err != nil {
		t.Fatal(err)
	}
	return r
}

// queueStaleBackoff has m queue and write a backoff change built on the breaker it holds.
func queueStaleBackoff(t *testing.T, m *Monitor, l *Loop, id suppliers.EndpointID) {
	t.Helper()
	e := m.Live(id, 1)
	e.mu.Lock()
	stale := e.record
	stale.Backoff = maxProbeBackoff
	queued := m.transition(e, stale, "", false)
	e.mu.Unlock()
	if err := l.flush(t.Context()); !queued || err != nil {
		t.Fatalf("the stale transition was not written: queued %v, %v", queued, err)
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

func storedBackoff(t *testing.T, l *Loop, id suppliers.EndpointID) (n int) {
	t.Helper()
	if err := l.db.ReadTx(t.Context(), l.scope, func(tx pg.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT backoff FROM quality.endpoint_health WHERE endpoint_id=$1`, id).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}
