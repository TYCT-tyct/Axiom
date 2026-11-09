package quality

import (
	"errors"
	"testing"
	"time"

	"axiom/internal/platform/pg"
	"axiom/internal/suppliers"
)

// TestResetHealthClosesTheBreaker: a reset of an offline endpoint closes its breaker in the store and, once adopted, on
// this node at once, with the slow start, the incident's signals resolved and a reset event; another node adopts it on
// the store when a transition it built on the old state loses, and does not probe; a rolled-back reset and a reset of a closed breaker change nothing.
func TestResetHealthClosesTheBreaker(t *testing.T) {
	m, l, f, now := fixtureLoop(t, Enforce)
	id := addProbeEndpoint(f)
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

	reset := func(commit bool) (r HealthReset) {
		t.Helper()
		err := l.db.Tx(t.Context(), l.scope, func(tx pg.Tx) error {
			var err error
			if r, err = ResetHealth(t.Context(), tx, id, *now); err != nil || commit {
				return err
			}
			return errRolledBack
		})
		if commit && err != nil {
			t.Fatal(err)
		}
		return r
	}
	reset(false)
	if at, _ := m.Live(id, 1).Routing(*now); at.IsZero() || resetEvents(t, l) != 0 {
		t.Fatal("a rolled-back reset changed the breaker")
	}

	m.AdoptReset(reset(true))
	e := m.Live(id, 1)
	if at, limit := e.Routing(*now); !at.IsZero() || limit != 1 || e.Offline(*now) {
		t.Fatalf("after the reset: opened %v, limit %d, offline %v", at, limit, e.Offline(*now))
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

	// Another node still holds the open breaker; a transition it queued on that state must lose to the reset.
	oe := other.Live(id, 1)
	oe.mu.Lock()
	stale := oe.record
	stale.Backoff = maxProbeBackoff
	queued := other.transition(oe, stale, "", false)
	oe.mu.Unlock()
	if err := otherLoop.flush(t.Context()); !queued || err != nil {
		t.Fatalf("the stale transition was not written: queued %v, %v", queued, err)
	}
	if at, _ := oe.Routing(*now); !at.IsZero() || storedBackoff(t, l, id) != 0 {
		t.Fatal("a transition built on the old breaker overwrote the reset")
	}

	*now = now.Add(10 * time.Minute)
	tick(t, otherLoop)
	if otherLoop.active != 0 {
		t.Fatal("another node probed an endpoint a person had reset")
	}
	if at, _ := other.Live(id, 1).Routing(*now); !at.IsZero() {
		t.Fatal("another node kept the breaker open after its probe claim lost to the reset")
	}

	if r := reset(true); r.done || resetEvents(t, l) != 1 {
		t.Fatal("a reset of a closed breaker changed it")
	}
}

var errRolledBack = errors.New("rolled back")

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
