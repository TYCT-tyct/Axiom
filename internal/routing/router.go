package routing

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"sync/atomic"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
)

// Config holds the router's settings. Defaults live here, in New; a zero field takes its default.
type Config struct {
	MaxStale      time.Duration // the oldest snapshot Plan will use; default 30s
	RetryAfter    time.Duration // the wait Plan suggests when every candidate is full; default 1s
	StickyTTL     time.Duration // how long a session stays stuck when its preset says nothing; default 1h
	StickyMax     int           // sessions remembered; default 100000
	SessionPrefix int           // bytes of the body SessionKey reads; default 16 KiB
	MaxAttempts   int           // attempts per request when the preset says nothing; default 4
	AttemptWindow time.Duration // how long after admission a new attempt may start; default 2m
	Recovery      time.Duration // how long a 429's weight damping takes to wear off; default 1m
	Pause         time.Duration // how long Block pauses an endpoint or supplier whose failure states no wait; default 5s (sub2api's defaultRateLimit429CooldownSeconds)
	ThrottleFloor float64       // the weight factor right after a 429; default 0.1
	RetryRatio    float64       // retries a supplier may receive per request it received; default 0.2
	RetryFloor    int64         // retries always allowed in a window, so a quiet supplier can be retried onto; default 3
	RetryWindow   time.Duration // the budget's window; default 1m

	// Segments are billing's settings for reserving in segments. An entry is reserved in segments only when its output
	// limit leaves room for a first segment plus the overshoot allowance at its own fastest speed, and its call lets the
	// gateway meter the output as it comes; otherwise it is reserved whole. A zero segment size segments none.
	Segments billing.Segments

	Now  func() time.Time // default time.Now
	Rand func() float64   // uniform in [0, 1); default math/rand/v2
}

// Router plans routes. It holds the current snapshot and the in-process state, and is safe for concurrent use.
type Router struct {
	cfg    Config
	snap   atomic.Pointer[Snapshot]
	state  *state
	sticky *sticky
	budget *budget
}

// New returns a router with no snapshot: Plan returns ErrStale until Replace gives it one.
func New(cfg Config) (*Router, error) {
	cfg.setDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Router{
		cfg: cfg, state: &state{endpoints: map[EndpointID]*epState{}, suppliers: map[SupplierID]*cooldown{}}, sticky: newSticky(cfg.StickyMax),
		budget: newBudget(cfg.RetryRatio, cfg.RetryFloor, cfg.RetryWindow),
	}, nil
}

func (c *Config) setDefaults() {
	c.MaxStale = orDefault(c.MaxStale, 30*time.Second)
	c.RetryAfter = orDefault(c.RetryAfter, time.Second)
	c.StickyTTL = orDefault(c.StickyTTL, time.Hour)
	c.AttemptWindow = orDefault(c.AttemptWindow, 2*time.Minute)
	c.Recovery = orDefault(c.Recovery, time.Minute)
	c.Pause = orDefault(c.Pause, 5*time.Second)
	c.RetryWindow = orDefault(c.RetryWindow, time.Minute)
	c.StickyMax = orDefault(c.StickyMax, 100000)
	c.SessionPrefix = orDefault(c.SessionPrefix, 16<<10)
	c.MaxAttempts = orDefault(c.MaxAttempts, 4)
	c.RetryFloor = orDefault(c.RetryFloor, 3)
	c.ThrottleFloor = orDefault(c.ThrottleFloor, 0.1)
	c.RetryRatio = orDefault(c.RetryRatio, 0.2)
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Rand == nil {
		c.Rand = rand.Float64
	}
}

// validate refuses a negative limit or duration, which a default does not replace (only zero does).
func (c *Config) validate() error {
	for _, d := range []time.Duration{c.MaxStale, c.RetryAfter, c.StickyTTL, c.AttemptWindow, c.Recovery, c.Pause, c.RetryWindow} {
		if d < 0 {
			return errors.New("routing: a duration is negative")
		}
	}
	for _, n := range []int64{int64(c.StickyMax), int64(c.SessionPrefix), int64(c.MaxAttempts), c.RetryFloor, c.Segments.Output} {
		if n < 0 {
			return errors.New("routing: a limit is negative")
		}
	}
	if c.Segments.Overshoot < 0 {
		return errors.New("routing: the overshoot allowance is negative")
	}
	if c.ThrottleFloor <= 0 || c.ThrottleFloor > 1 || c.RetryRatio < 0 {
		return fmt.Errorf("routing: the throttle floor %v must be in (0, 1] and the retry ratio %v not negative", c.ThrottleFloor, c.RetryRatio)
	}
	return nil
}

func orDefault[T int | int64 | float64 | time.Duration](v, fallback T) T {
	if v == 0 {
		return fallback
	}
	return v
}

// Fresh reports whether Plan would use the current snapshot: there is one and it is not older than MaxStale. It is the
// one staleness test, so a node that reports itself ready is a node whose calls are not all answered ErrStale.
func (r *Router) Fresh() bool {
	snap := r.snap.Load()
	return snap != nil && r.fresh(snap, r.cfg.Now())
}

func (r *Router) fresh(s *Snapshot, now time.Time) bool { return now.Sub(s.built) <= r.cfg.MaxStale }

// ServesFor reports whether an endpoint has a published price for this account and its admission tier.
// It ignores transient state and snapshot freshness, so a busy or stale router keeps its model list.
func (r *Router) ServesFor(m catalog.ModelID, a billing.Audience) bool {
	snap := r.snap.Load()
	if snap == nil {
		return false
	}
	for _, c := range snap.byModel[m] {
		if p := c.priceFor(a); p != nil && !p.none() {
			return true
		}
	}
	return false
}

// Replace makes s the snapshot Plan uses, if it is newer than the current one: a build that finishes late never
// replaces a newer one.
func (r *Router) Replace(s *Snapshot) {
	for {
		old := r.snap.Load()
		if old != nil && !s.built.After(old.built) {
			return
		}
		if r.snap.CompareAndSwap(old, s) {
			return
		}
	}
}
