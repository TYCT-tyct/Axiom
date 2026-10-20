package quality

import (
	"context"

	"axiom/internal/protocol"
	"axiom/internal/suppliers"
)

// verdict is what one attempt says of its endpoint's health.
type verdict uint8

const (
	uncounted verdict = iota // says nothing of the endpoint: neither a failure nor a success
	success
	failure
	auxiliary // a failure that only timed out: it counts, but H2's breaker never trips on these alone
)

// judge is the table of docs/domains/quality.md「什么算一次失败」, the only one: an attempt is judged by what it says of
// the endpoint, never by the request, and anything in doubt is not counted, since a failure counted wrongly lets one
// tenant take an endpoint away from everyone. The order matters: a client that left, and the call's own end, explain
// any failure that follows them.
func (m *Monitor) judge(ctx context.Context, o *Observation) verdict {
	switch {
	case o.Gone && o.committed(): // the endpoint was answering when the client left
		return success
	case o.Gone, o.Cut == Ours:
		return uncounted
	case o.Failure != nil:
		return m.refused(ctx, o)
	case o.Done && o.Empty:
		return failure
	case o.Done:
		return success
	case o.Cut == Idle && o.committed():
		return failure
	case o.Cut == Idle:
		return slow(o)
	}
	return byFault(o.Fault)
}

// refused judges an attempt the supplier answered with a failure, or did not answer. Only the endpoint's scope can
// count: a credential, a group or a supplier at fault is suppliers' and routing's to rest or pause, the route is H4's,
// and the request is the client's.
func (m *Monitor) refused(ctx context.Context, o *Observation) verdict {
	f := o.Failure
	if u, ok := f.Cause.(suppliers.Unknown); ok && u.SuspectedEgressBlock {
		m.suspectedBlock.Add(ctx, 1) // no attributes, so nothing is allocated
		return uncounted
	}
	if f.Scope != suppliers.ScopeEndpoint {
		return uncounted
	}
	switch c := f.Cause.(type) {
	case suppliers.UpstreamError, suppliers.Overloaded, suppliers.ModelNotFound:
		return failure
	case suppliers.Unknown:
		if c.Status >= 500 {
			return failure
		}
		m.unclassified.Add(ctx, 1) // most likely the request's (a 413 for a huge one); the adapter is to learn it
		return uncounted
	case suppliers.Transport:
		if o.Cut == HeaderWait {
			return slow(o)
		}
		return failure // the request was written, and nothing usable came back
	case suppliers.RateLimited, suppliers.ContextTooLong, suppliers.BadRequest, suppliers.ContentRejected,
		suppliers.NotPermitted, suppliers.CredentialRejected, suppliers.CredentialQuotaExhausted, suppliers.SupplierBalanceLow:
		return uncounted
	}
	return uncounted
}

// slow judges a timeout: a long input may take longer than the wait to prefill, and hidden reasoning may send nothing
// for as long, so only a short one counts, and only as an auxiliary failure.
func slow(o *Observation) verdict {
	if o.InputTokens <= slowMaxInput {
		return auxiliary
	}
	return uncounted
}

// byFault judges a stream that failed after its 2xx: broken, or ended by the upstream's own error, is the endpoint's;
// an error event of any other class, and a tool input the model got wrong, are not.
func byFault(f protocol.Fault) verdict {
	switch f {
	case protocol.FaultBroken, protocol.FaultServer:
		return failure
	case protocol.FaultNone, protocol.FaultRateLimited, protocol.FaultContextTooLong, protocol.FaultModeration,
		protocol.FaultBadRequest, protocol.FaultUnrecognised, protocol.FaultToolInput:
	}
	return uncounted
}
