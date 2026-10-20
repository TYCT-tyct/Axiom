package kv

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeClock is a clock a test moves by hand: the breaker's window is a duration, and waiting for it would make these
// tests slow and flaky (CONTRIBUTING.md「检查清单」「时序」).
type fakeClock struct{ at time.Time }

func newFakeClock() *fakeClock { return &fakeClock{at: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time          { return c.at }
func (c *fakeClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// opened is a breaker that the error rate has opened: twenty accesses in the window, more than half of them unavailable.
func opened(t *testing.T) (*breaker, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	b := newBreaker()
	for i := range rateAccesses {
		result := ok
		if i%2 == 0 {
			result = unavailable // ten of the twenty: exactly half, which is not enough
		}
		if _, open := b.done(clock.Now(), false, result); open {
			t.Fatalf("the breaker opened with %d accesses, half of them unavailable", i+1)
		}
	}
	if _, open := b.done(clock.Now(), false, unavailable); !open {
		t.Fatal("the breaker stayed closed with twenty-one accesses, eleven of them unavailable")
	}
	return b, clock
}

// TestBreakerOpensOnTheErrorRate: more than half of at least twenty accesses in the window are unavailable, and half is
// not enough; an open breaker does no I/O for its open period.
func TestBreakerOpensOnTheErrorRate(t *testing.T) {
	t.Parallel()
	b, clock := opened(t)
	if probe, allowed := b.allow(clock.Now()); allowed || probe {
		t.Fatal("an open breaker let an access through inside its open period")
	}
	clock.advance(openFor - time.Nanosecond)
	if _, allowed := b.allow(clock.Now()); allowed {
		t.Fatal("an open breaker let an access through before its open period ended")
	}
}

// TestBreakerHalfOpen: after the open period one probe goes out and the rest are refused; the probe's success closes the
// breaker, and its failure opens it for another period.
func TestBreakerHalfOpen(t *testing.T) {
	t.Parallel()
	b, clock := opened(t)
	clock.advance(openFor)
	if probe, allowed := b.allow(clock.Now()); !allowed || !probe {
		t.Fatal("the half-open breaker did not let one probe through")
	}
	if _, allowed := b.allow(clock.Now()); allowed {
		t.Fatal("the half-open breaker let a second access through")
	}
	if changed, open := b.done(clock.Now(), true, unavailable); changed || !open {
		t.Fatal("a failed probe did not leave the breaker open")
	}
	if _, allowed := b.allow(clock.Now()); allowed {
		t.Fatal("a failed probe did not start a new open period")
	}
	clock.advance(openFor)
	if probe, allowed := b.allow(clock.Now()); !allowed || !probe {
		t.Fatal("the second open period did not end in a probe")
	}
	if changed, open := b.done(clock.Now(), true, ok); !changed || open {
		t.Fatal("a successful probe did not close the breaker")
	}
	if _, allowed := b.allow(clock.Now()); !allowed {
		t.Fatal("a closed breaker refused an access")
	}
}

// TestBreakerOpensOnSlowAccesses: a fifth of at least ten accesses ended on our own Timeout, which the error rate cannot
// see when only part of the traffic is slowed.
func TestBreakerOpensOnSlowAccesses(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	b := newBreaker()
	for range slowAccesses {
		b.done(clock.Now(), false, ok)
	}
	for slow, wantOpen := range []bool{false, false, true} {
		if _, open := b.done(clock.Now(), false, timedOut); open != wantOpen {
			t.Fatalf("after %d slow accesses in %d: open = %v", slow+1, slowAccesses+slow+1, open)
		}
	}
}

// TestBreakerOpensOnAStreak: five unavailable accesses in a row are enough, however quiet the node is.
func TestBreakerOpensOnAStreak(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	b := newBreaker()
	for i := range streakAccesses {
		_, open := b.done(clock.Now(), false, unavailable)
		if open != (i == streakAccesses-1) {
			t.Fatalf("access %d: open = %v", i+1, open)
		}
	}
}

// TestBreakerForgetsOldAccesses: a slot that has not been written for the window still holds the accesses of its own
// second, and tripped must not count them ten seconds on, or one burst would count against the server for ever.
func TestBreakerForgetsOldAccesses(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	b := newBreaker()
	for range rateAccesses {
		b.record(clock.Now(), unavailable)
	}
	if !b.tripped(clock.Now()) {
		t.Fatal("twenty unavailable accesses in one second did not trip the window")
	}
	clock.advance(windowBuckets * time.Second)
	if b.tripped(clock.Now()) {
		t.Fatal("tripped on the accesses of ten seconds ago")
	}
}

// TestBreakerIgnoresSkipsAndOurMistakes: a client that disconnects, and a script the server refused, say nothing about
// the server's health.
func TestBreakerIgnoresSkipsAndOurMistakes(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	b := newBreaker()
	for range 10 * rateAccesses {
		b.done(clock.Now(), false, skipped)
		b.done(clock.Now(), false, failed)
	}
	if probe, allowed := b.allow(clock.Now()); !allowed || probe {
		t.Fatal("skips and refused commands opened the breaker")
	}
}

// TestOutcomeTellsTheCallersFailure: only our own Timeout and a connection error count, which is what keeps a client's
// cancel from opening the breaker on everyone's behalf.
func TestOutcomeTellsTheCallersFailure(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, stop := context.WithTimeout(t.Context(), 0)
	defer stop()
	alive := func() context.Context { return t.Context() }
	for _, tt := range []struct {
		name   string
		caller func() context.Context
		err    error
		want   outcome
	}{
		{"done", alive, nil, ok},
		{"our timeout", alive, context.DeadlineExceeded, timedOut},
		{"the caller's deadline", func() context.Context { return expired }, context.DeadlineExceeded, skipped},
		{"a cancel", func() context.Context { return cancelled }, context.Canceled, skipped},
		{"a connection error", alive, errors.New("dial: connection refused"), unavailable},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := outcomeOf(tt.caller(), tt.err); got != tt.want {
				t.Fatalf("outcome = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBreakerCancelledProbeGivesItsPlaceUp: the access that got the probe's place and was cancelled by its caller learned
// nothing, so it frees the place, counts no failure and leaves the open period alone; the next access probes and its
// success closes the breaker. Without that the half-open breaker refused everything for ever.
func TestBreakerCancelledProbeGivesItsPlaceUp(t *testing.T) {
	t.Parallel()
	b, clock := opened(t)
	clock.advance(openFor)
	if probe, allowed := b.allow(clock.Now()); !probe || !allowed {
		t.Fatal("no probe after the open period")
	}
	if changed, open := b.done(clock.Now(), true, skipped); changed || open {
		t.Fatalf("a cancelled probe changed the breaker: changed %v, open %v", changed, open)
	}
	if !b.open || b.streak != 0 {
		t.Fatalf("a cancelled probe counted as a failure: open %v, streak %d", b.open, b.streak)
	}
	probe, allowed := b.allow(clock.Now())
	if !probe || !allowed {
		t.Fatal("the next access after a cancelled probe was not given the probe")
	}
	if changed, open := b.done(clock.Now(), probe, ok); !changed || open {
		t.Fatal("the next probe's success did not close the breaker")
	}
}
