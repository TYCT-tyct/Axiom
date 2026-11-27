package suppliers

import (
	"time"

	"axiom/internal/suppliers/internal/adapter"
)

// What a failed attempt rests: the supplier on a balance refusal, or the credential at fault.

// rest pauses the whole supplier on a balance refusal, and an individual credential until its stated reset or
// configured cooldown, or a key that is its own wallet until a read of its balance shows funds. Other faults are for
// routing and quality.
func (a *Attempt) rest(f Failure) {
	if _, low := f.Cause.(adapter.SupplierBalanceLow); low && f.Scope == adapter.ScopeSupplier {
		a.ep.funds.rest(a.s.now())
		return
	}
	if f.Scope != adapter.ScopeCredential {
		return
	}
	until, rests := a.restUntil(f.Cause, a.s.now())
	if !rests {
		return
	}
	if _, refused := f.Cause.(adapter.CredentialRejected); refused {
		a.s.reportRejected(a.ep.supplier, a.cred)
	}
	for {
		cur := a.cred.rt.restUnix.Load()
		if until.UnixNano() <= cur || a.cred.rt.restUnix.CompareAndSwap(cur, until.UnixNano()) {
			return
		}
	}
}

// restUntil is when the credential may be chosen again after a fault of its own: its stated reset, a rate limit's
// retry, or the configured cooldown. False for a key that is its own wallet and is out of funds: it rests until a read
// of its balance shows some, not for a time.
func (a *Attempt) restUntil(cause adapter.Cause, now time.Time) (time.Time, bool) {
	until := now.Add(a.s.cfg.CredentialCooldown)
	switch c := cause.(type) {
	case adapter.CredentialQuotaExhausted:
		if a.cred.funds != nil {
			a.cred.rt.broke.Store(now.UnixNano())
			return time.Time{}, false
		}
		until = now.Add(a.s.cfg.QuotaCooldown)
		if c.ResetAt.After(now) {
			until = c.ResetAt
		}
	case adapter.RateLimited:
		until = now.Add(max(c.RetryAfter, time.Second))
	case adapter.CredentialRejected, adapter.Overloaded, adapter.UpstreamError, adapter.SupplierBalanceLow, adapter.NotPermitted,
		adapter.ContextTooLong, adapter.BadRequest, adapter.ContentRejected, adapter.ModelNotFound, adapter.Unknown, adapter.Transport:
	}
	return until, true
}
