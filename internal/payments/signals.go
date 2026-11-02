package payments

import (
	"strconv"
	"sync"
	"time"
)

// The signal kinds of the per-event conditions docs/domains/payments.md「审计与告警」 promises. The two durable
// business signals, payments.discrepancy and payments.query_paused, are written in their own transactions; these four
// describe a rate over time, so each node counts them in one-minute windows and reports the completed window at most
// once.
const (
	signalVerifyFailed       = "payments.verify_failed"
	signalQueryFailed        = "payments.query_failed"
	signalRateLimited        = "payments.rate_limited"
	signalMerchantUnreadable = "payments.merchant_unreadable"
)

// signalWindow is the counting window. The producer reports the full count and lets the administrator's alert rule
// decide the threshold; a complete window with no event resolves the incident.
const signalWindow = time.Minute

type signalKey struct{ kind, key string }

// SignalState is one node's signal counters and unresolved merchant incidents. Every Payments on a node shares one
// state (Config.Signals) so a listener, a background job and a short-lived admin service count and resolve the same
// key. The map is bounded by the signal kinds, sources and merchant accounts that exist.
type SignalState struct {
	mu         sync.Mutex
	counters   map[signalKey]*signalCounter
	unreadable map[string]struct{}
}

// NewSignalState is the node-local state Open creates when Config.Signals is nil.
func NewSignalState() *SignalState {
	return &SignalState{
		counters:   make(map[signalKey]*signalCounter),
		unreadable: make(map[string]struct{}),
	}
}

// signalCounter is one window of one signal. reported suppresses a second Raise in the same window; a signal the
// bounded sink refused is best-effort and the next window reports its own count.
type signalCounter struct {
	start    time.Time
	count    int
	reported bool
	firing   bool
}

// verificationFailed counts a callback whose signature did not verify.
func (p *Payments) verificationFailed() {
	p.countSignal(signalVerifyFailed, "callbacks")
}

// queryFailed counts a merchant query whose outcome was unknown. Order and refund queries share the same health rule.
func (p *Payments) queryFailed() {
	p.countSignal(signalQueryFailed, "queries")
}

// rateLimited counts a callback or an order refused by one of the ingress limits.
func (p *Payments) rateLimited(source string) {
	p.countSignal(signalRateLimited, source)
}

// countSignal is the spike mechanism: bounded memory (one counter per kind and key), no database write per event, and
// one Raise at most per window. It reports the full count so the administrator's rule threshold, not this code, decides
// whether the window alerts; a complete window with no event resolves the incident.
func (p *Payments) countSignal(kind, source string) {
	if p.alerts == nil {
		return
	}
	now := p.now()
	p.signals.mu.Lock()
	defer p.signals.mu.Unlock()
	k := signalKey{kind: kind, key: p.instance + "/" + source}
	c := p.signals.counters[k]
	if c == nil {
		c = &signalCounter{start: now.Truncate(signalWindow)}
		p.signals.counters[k] = c
		p.afterReport(k, c.start)
	} else if !now.Before(c.start.Add(signalWindow)) {
		p.report(k, c)
		c.start, c.count, c.reported = now.Truncate(signalWindow), 0, false
		p.afterReport(k, c.start)
	}
	c.count++
}

// afterReport flushes the window at its end so a burst that stops is still reported. A later event on the next window
// also reports it, whichever comes first; the start time makes the two paths idempotent.
func (p *Payments) afterReport(k signalKey, start time.Time) {
	delay := start.Add(signalWindow).Sub(p.now())
	if delay < 0 {
		delay = 0
	}
	p.after(delay, func() { p.flush(k, start) })
}

// flush reports a completed window. A timer that fires early waits for the rest of the window; one that fires after a
// newer event started another window does nothing.
func (p *Payments) flush(k signalKey, start time.Time) {
	p.signals.mu.Lock()
	defer p.signals.mu.Unlock()
	c := p.signals.counters[k]
	if c == nil || !c.start.Equal(start) || c.reported {
		return
	}
	if now := p.now(); now.Before(start.Add(signalWindow)) {
		p.after(start.Add(signalWindow).Sub(now), func() { p.flush(k, start) })
		return
	}
	p.report(k, c)
}

// report raises the window's full count and schedules the recovery check at the end of the next window. If that window
// gets an event it replaces start, so the check does nothing; a different node's key is never touched.
func (p *Payments) report(k signalKey, c *signalCounter) {
	if c.count == 0 || c.reported {
		return
	}
	if p.alerts.Raise(k.kind, k.key, "warning", map[string]string{"value": strconv.Itoa(c.count)}) != nil {
		return
	}
	c.reported, c.firing = true, true
	start := c.start
	delay := start.Add(2 * signalWindow).Sub(p.now())
	if delay < 0 {
		delay = 0
	}
	p.after(delay, func() { p.recover(k, start) })
}

// recover resolves a node's incident after a complete window with no event. A new event in that window moves start, so
// an active node's incident stays open and a different node never resolves this one.
func (p *Payments) recover(k signalKey, start time.Time) {
	p.signals.mu.Lock()
	defer p.signals.mu.Unlock()
	c := p.signals.counters[k]
	if c == nil || !c.start.Equal(start) || !c.firing {
		return
	}
	if p.alerts.Resolve(k.kind, k.key) == nil {
		c.firing = false
	}
}

// merchantUnreadable raises once per merchant on this node, not once per callback, and only after the sink accepted the
// signal. The state is bounded by the merchant accounts stored in the database: only a merchant that exists can fail to
// unseal.
func (p *Payments) merchantUnreadable(id MerchantID) {
	if p.alerts == nil {
		return
	}
	key := p.instance + "/" + id.TypeID()
	p.signals.mu.Lock()
	defer p.signals.mu.Unlock()
	if _, ok := p.signals.unreadable[key]; ok {
		return
	}
	if p.alerts.Raise(signalMerchantUnreadable, key, "critical", map[string]string{"value": "1"}) == nil {
		p.signals.unreadable[key] = struct{}{}
	}
}

// merchantReadable resolves an open unreadable signal after the merchant's configuration opens again. The lock is held
// across Resolve so a concurrent failure cannot raise a new incident that this stale success then resolves.
func (p *Payments) merchantReadable(id MerchantID) {
	if p.alerts == nil {
		return
	}
	key := p.instance + "/" + id.TypeID()
	p.signals.mu.Lock()
	defer p.signals.mu.Unlock()
	if _, ok := p.signals.unreadable[key]; !ok {
		return
	}
	if p.alerts.Resolve(signalMerchantUnreadable, key) == nil {
		delete(p.signals.unreadable, key)
	}
}
