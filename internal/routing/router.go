package routing

import (
	"cmp"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric"

	"axiom/internal/accounts"
	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/platform/alert"
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

	// Segments are billing's live settings for reserving in segments, the same policy billing reserves by. An entry is
	// reserved in segments only when its output limit leaves room for a first segment plus the overshoot allowance at
	// its own fastest speed, and its call lets the gateway meter the output as it comes; otherwise it is reserved
	// whole. Nil, or a zero segment size, segments none.
	Segments *billing.SegmentPolicy

	// Meter counts the calls refused because of their key's preset; nil counts nowhere. Alerts is told of a platform
	// or site preset that refuses every call it sees, under keys prefixed with Node, this node's name; nil watches no
	// preset (presetWatch).
	Meter  metric.Meter
	Alerts alert.Sink
	Node   string

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
	// suspended is the platform's kill switch as Presets.Load last read it. It is apart from the snapshot so that it
	// holds from the moment it is read, even when the rest of that build fails and the last snapshot stands.
	suspended atomic.Bool
	watch     *presetWatch
}

// New returns a router with no snapshot: Plan returns ErrStale until Replace gives it one.
func New(cfg Config) (*Router, error) {
	cfg.setDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	watch, err := newPresetWatch(cfg)
	if err != nil {
		return nil, err
	}
	return &Router{
		cfg: cfg, state: &state{endpoints: map[EndpointID]*epState{}, suppliers: map[SupplierID]*cooldown{}}, sticky: newSticky(cfg.StickyMax),
		budget: newBudget(cfg.RetryRatio, cfg.RetryFloor, cfg.RetryWindow), watch: watch,
	}, nil
}

func (c *Config) setDefaults() {
	c.MaxStale = cmp.Or(c.MaxStale, 30*time.Second)
	c.RetryAfter = cmp.Or(c.RetryAfter, time.Second)
	c.StickyTTL = cmp.Or(c.StickyTTL, time.Hour)
	c.AttemptWindow = cmp.Or(c.AttemptWindow, 2*time.Minute)
	c.Recovery = cmp.Or(c.Recovery, time.Minute)
	c.Pause = cmp.Or(c.Pause, 5*time.Second)
	c.RetryWindow = cmp.Or(c.RetryWindow, time.Minute)
	c.StickyMax = cmp.Or(c.StickyMax, 100000)
	c.SessionPrefix = cmp.Or(c.SessionPrefix, 16<<10)
	c.MaxAttempts = cmp.Or(c.MaxAttempts, 4)
	c.RetryFloor = cmp.Or(c.RetryFloor, 3)
	c.ThrottleFloor = cmp.Or(c.ThrottleFloor, 0.1)
	c.RetryRatio = cmp.Or(c.RetryRatio, 0.2)
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
	for _, n := range []int64{int64(c.StickyMax), int64(c.SessionPrefix), int64(c.MaxAttempts), c.RetryFloor} {
		if n < 0 {
			return errors.New("routing: a limit is negative")
		}
	}
	if c.Alerts != nil && c.Node == "" {
		return errors.New("routing: an alert sink needs the node's name, which tells one node's signals from another's")
	}
	if c.ThrottleFloor <= 0 || c.ThrottleFloor > 1 || c.RetryRatio < 0 {
		return fmt.Errorf("routing: the throttle floor %v must be in (0, 1] and the retry ratio %v not negative", c.ThrottleFloor, c.RetryRatio)
	}
	return nil
}

// Fresh reports whether Plan would use the current snapshot: there is one and it is not older than MaxStale. It is the
// one staleness test, so a node that reports itself ready is a node whose calls are not all answered ErrStale.
func (r *Router) Fresh() bool {
	snap := r.snap.Load()
	return snap != nil && r.fresh(snap, r.cfg.Now())
}

func (r *Router) fresh(s *Snapshot, now time.Time) bool { return now.Sub(s.built) <= r.cfg.MaxStale }

// KeyView is what the model lists may say a key can reach (docs/domains/routing.md「请求覆盖与组合」): the snapshot's
// endpoints the key can see, priced for its account and tier, that its preset's rule for the model passes. It applies
// the rule's hard conditions only, by the same presetAllows Plan applies, and never a request's: the price limit is
// held at the standard tier and the shortest prompt, the price the list shows. Nothing transient (load, breakers,
// pauses, the snapshot's age) enters it, so a model does not come and go from the list with load.
type KeyView struct {
	snap   *Snapshot
	preset *preset
	a      Admission
}

// KeyView is the key's view of the current snapshot. A router without a snapshot shows nothing. A preset the snapshot
// does not hold, or one not of the key's layers or currency, is ErrPresetUnavailable, as Plan refuses it: the key can
// call nothing, and a list of models would say it can.
func (r *Router) KeyView(a Admission) (KeyView, error) {
	snap := r.snap.Load()
	if snap == nil {
		return KeyView{}, nil
	}
	p, err := r.presetOf(snap, a)
	if err != nil {
		return KeyView{}, err
	}
	return KeyView{snap: snap, preset: p, a: a}, nil
}

// presetOf is the preset the key routes by in snap: ErrPresetUnavailable when snap does not hold it, when it is not of
// the key's layers or currency, or when it is not the system preset and the platform has suspended presets.
func (r *Router) presetOf(snap *Snapshot, a Admission) (*preset, error) {
	p, ok := snap.presets[a.Preset]
	if !ok || !p.visibleTo(a) || a.Preset != SystemPreset && r.suspended.Load() {
		return nil, ErrPresetUnavailable
	}
	return p, nil
}

// suspendPresets sets the platform's kill switch (SetSuspension) as Presets.Load read it, and reports whether that
// changed it.
func (r *Router) suspendPresets(on bool) bool { return r.suspended.Swap(on) != on }

// Serves reports whether some endpoint of the model is in the key's view.
func (v KeyView) Serves(m catalog.ModelID) bool {
	if v.snap == nil {
		return false
	}
	return slices.ContainsFunc(v.snap.byModel[m], func(c *candidate) bool {
		return c.Owner.visibleTo(v.a) && v.allows(c)
	})
}

// Allows reports whether endpoint e of the model is in the key's view, whoever owns it.
func (v KeyView) Allows(m catalog.ModelID, e EndpointID) bool {
	if v.snap == nil {
		return false
	}
	i := slices.IndexFunc(v.snap.byModel[m], func(c *candidate) bool { return c.ID == e })
	return i >= 0 && v.allows(v.snap.byModel[m][i])
}

func (v KeyView) allows(c *candidate) bool {
	price := c.viewPrice(v.a)
	return price != nil && viewAllows(v.preset, v.preset.ruleFor(c.ModelSlug), c, price)
}

// viewPrice is the endpoint's price for the admission's account and tier, nil when it has none for them.
func (c *candidate) viewPrice(a Admission) *candidatePrice {
	price := c.priceFor(billing.Audience{Account: accounts.AccountID(a.Account), Tier: a.Tier})
	if price == nil || price.none() {
		return nil
	}
	return price
}

// viewAllows is whether rule r of preset p keeps the endpoint at price as a view judges it: its hard conditions only,
// the price at the standard tier and the shortest prompt.
func viewAllows(p *preset, r *Rule, c *candidate, price *candidatePrice) bool {
	n := need{preset: p, rule: r, tiers: 1 << catalog.ServiceTierStandard}
	return n.presetAllows(c, price, Spec{Context: c.Context})
}

// Replace makes s the snapshot Plan uses, if it is newer than the current one: a build that finishes late never
// replaces a newer one. It runs once a rebuild, which is also the clock the presets' windows close by.
func (r *Router) Replace(s *Snapshot) {
	r.watch.tick(r.cfg.Now())
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
