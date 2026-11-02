package payments

import (
	"strconv"
	"time"
)

// The signal kinds of the per-event conditions docs/domains/payments.md「审计与告警」 promises. The two durable
// business signals, payments.discrepancy and payments.query_paused, are written in their own transactions; these four
// describe a rate over time, so one process counts them in one-minute windows and raises at most once per window.
const (
	signalVerifyFailed       = "payments.verify_failed"
	signalQueryFailed        = "payments.query_failed"
	signalRateLimited        = "payments.rate_limited"
	signalMerchantUnreadable = "payments.merchant_unreadable"
)

// signalWindow is the counting window. A floor is the count in one window at which the signal is raised; an
// administrator's alert rule can only be stricter, because a count below its floor is never emitted.
const (
	signalWindow = time.Minute
	verifyFloor  = 10
	queryFloor   = 5
	rateFloor    = 20
)

type signalKey struct{ kind, key string }

// signalCounter is one window of one signal. raised is per window so a sink that refused a signal is retried on the
// next event instead of being lost for the rest of the window.
type signalCounter struct {
	start  time.Time
	count  int
	raised bool
	firing bool
}

// verificationFailed counts a callback whose signature did not verify.
func (p *Payments) verificationFailed() {
	p.countSignal(signalVerifyFailed, "callbacks", "warning", verifyFloor)
}

// queryFailed counts a merchant query whose outcome was unknown. Order and refund queries share the same health rule.
func (p *Payments) queryFailed() {
	p.countSignal(signalQueryFailed, "queries", "warning", queryFloor)
}

// rateLimited counts a callback or an order refused by one of the ingress limits.
func (p *Payments) rateLimited(source string) {
	p.countSignal(signalRateLimited, source, "warning", rateFloor)
}

// countSignal is the spike mechanism: bounded memory (one counter per kind and key), no database write per event, and
// one Raise at most per window, when the count reaches floor. A window below the floor resolves an open incident; the
// signal is raised again when a later window reaches the floor.
func (p *Payments) countSignal(kind, key, severity string, floor int) {
	if p.alerts == nil {
		return
	}
	now := p.now()
	p.signalMu.Lock()
	defer p.signalMu.Unlock()
	k := signalKey{kind: kind, key: key}
	c := p.signals[k]
	if c == nil {
		c = &signalCounter{start: now.Truncate(signalWindow)}
		p.signals[k] = c
	} else if !now.Before(c.start.Add(signalWindow)) {
		if c.firing && c.count < floor && p.alerts.Resolve(kind, key) == nil {
			c.firing = false
		}
		c.start, c.count, c.raised = now.Truncate(signalWindow), 0, false
	}
	c.count++
	if c.raised || c.count < floor {
		return
	}
	if p.alerts.Raise(kind, key, severity, map[string]string{"value": strconv.Itoa(c.count)}) == nil {
		c.raised, c.firing = true, true
	}
}

// merchantUnreadable raises once per merchant, not once per callback, and only after the sink accepted the signal. The
// map is bounded by the merchant accounts stored in the database: only a merchant that exists can fail to unseal.
func (p *Payments) merchantUnreadable(id MerchantID) {
	if p.alerts == nil {
		return
	}
	p.signalMu.Lock()
	defer p.signalMu.Unlock()
	if _, ok := p.unreadable[id]; ok {
		return
	}
	if p.alerts.Raise(signalMerchantUnreadable, id.TypeID(), "critical", map[string]string{"value": "1"}) == nil {
		p.unreadable[id] = struct{}{}
	}
}

// merchantReadable resolves an open unreadable signal after the merchant's configuration opens again.
func (p *Payments) merchantReadable(id MerchantID) {
	if p.alerts == nil {
		return
	}
	p.signalMu.Lock()
	_, raised := p.unreadable[id]
	delete(p.unreadable, id)
	p.signalMu.Unlock()
	if raised && p.alerts.Resolve(signalMerchantUnreadable, id.TypeID()) != nil {
		p.signalMu.Lock()
		p.unreadable[id] = struct{}{}
		p.signalMu.Unlock()
	}
}
