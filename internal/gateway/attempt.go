package gateway

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/protocol"
	"axiom/internal/routing"
	"axiom/internal/suppliers"
)

// The causes an attempt is cancelled for, which tell what ended its read (context.Cause).
var (
	errIdle       = errors.New("gateway: the upstream sent nothing new in time")
	errHeaderWait = errors.New("gateway: the upstream did not answer in time")
	errClientGone = errors.New("gateway: the client is not reading")
	errShutdown   = errors.New("gateway: the node is going away")
)

// skips is why entries were passed over without an attempt (internal/gateway/README.md「尝试」).
type skips struct {
	noSlot, noEndpoint bool
	unsupported        string               // the first reason an endpoint could not express the request
	failedSuppliers    []routing.SupplierID // stay excluded for this request even after their pause ends
}

// step is what the attempt loop does after an entry.
type step uint8

const (
	nextEntry step = iota // the next entry of the route
	sameEntry             // the entry again, with another credential
	over                  // the call has ended
)

// attempts tries the route's entries in order until one answers, none is left, or something ends the call.
func (s *session) attempts(ctx context.Context) ended {
	maxAttempts := min(s.c.Plan.MaxAttempts, platform.MaxAttempts)
	for i := 0; i < len(s.c.Plan.Entries); {
		if e, stop := s.mayStart(maxAttempts); stop {
			return s.left(e)
		}
		next, e := s.try(ctx, i)
		switch next {
		case over:
			return e
		case nextEntry:
			i++
		case sameEntry:
		}
	}
	return s.left(s.exhausted())
}

// mayStart says whether another attempt may begin, and if not how the call ends. A client that has gone gets nothing,
// a holder that may be gone, or a call at the end of its time, no new attempt (the reservation may no longer cover it),
// and a call past StartBy or on a node going away none either.
func (s *session) mayStart(maxAttempts int) (ended, bool) {
	switch {
	case s.req.Context().Err() != nil:
		return ended{}, true
	case s.x.shutdown.Err() != nil:
		return ended{err: protocol.Error{Type: protocol.Timeout}}, true
	case !s.x.Alive():
		return ended{err: protocol.Error{Type: protocol.Overloaded}, after: 1}, true
	case !time.Now().Before(s.deadline):
		return ended{err: protocol.Error{Type: protocol.Timeout}}, true
	case len(s.ends) >= maxAttempts:
		return s.exhausted(), true
	case time.Now().After(s.c.Plan.StartBy):
		if len(s.ends) == 0 {
			return ended{err: protocol.Error{Type: protocol.Timeout}}, true
		}
		return s.exhausted(), true
	}
	return ended{}, false
}

// exhausted is how a call ends when no entry is left to try. After an attempt it is the upstream's failure whatever
// the last one was (docs/domains/gateway.md「错误」); with none, why every entry was passed over: a credential slot
// that was taken is worth a retry in a second, an endpoint the suppliers do not know is the node's trouble, and what
// is left is a request no endpoint can express.
func (s *session) exhausted() ended {
	switch {
	case len(s.ends) > 0:
		return ended{err: protocol.Error{Type: protocol.UpstreamFailed}}
	case s.skips.noSlot:
		return ended{err: protocol.Error{Type: protocol.RateLimited}, after: 1}
	case s.skips.noEndpoint:
		return ended{err: protocol.Error{Type: protocol.Overloaded}, after: 1}
	}
	return ended{err: protocol.Error{Type: protocol.InvalidRequest, Message: s.skips.unsupported}}
}

// try makes the attempt on entry i, or passes the entry over: no credential to give, a request it cannot express, or a
// retry its supplier's budget does not allow. Only an attempt counts.
func (s *session) try(ctx context.Context, i int) (step, ended) {
	entry := s.c.Plan.Entries[i]
	if slices.Contains(s.skips.failedSuppliers, entry.Supplier) || s.x.Router.Blocked(entry) {
		s.skips.noEndpoint = true
		s.exclude(entry, routing.CoolingDown)
		return nextEntry, ended{}
	}
	conn, err := s.x.Upstreams.Acquire(uuid.UUID(entry.Endpoint))
	if err != nil {
		s.skips.noSlot = s.skips.noSlot || errors.Is(err, suppliers.ErrNoSlot)
		s.skips.noEndpoint = s.skips.noEndpoint || !errors.Is(err, suppliers.ErrNoSlot)
		s.exclude(entry, routing.Full)
		return nextEntry, ended{}
	}
	up, held, err := s.prepare(entry)
	if errors.Is(err, ErrOverloaded) {
		conn.Done()
		return over, ended{err: protocol.Error{Type: protocol.Overloaded}, after: 1}
	}
	if unsupported, ok := errors.AsType[*protocol.NotSupported](err); ok {
		conn.Done()
		if s.skips.unsupported == "" {
			s.skips.unsupported = unsupported.Reason
		}
		s.exclude(entry, routing.FormatNotServed)
		return nextEntry, ended{}
	} else if err != nil {
		conn.Done()
		return over, s.unprepared(err)
	}
	if len(s.ends) == 0 {
		s.x.Router.Sent(entry.Supplier)
	} else if !s.x.Router.TakeRetry(entry.Supplier) {
		conn.Done()
		s.x.Memory.Refund(held)
		s.exclude(entry, routing.RetryBudget)
		return nextEntry, ended{}
	}
	return s.attempt(ctx, i, entry, conn, up, held)
}

// prepare makes the request ready for an endpoint within the node's memory budget. What Prepare may hold at once is
// charged before it allocates (a charge made after would be an allocation already done), and what stays, the capacity
// of the request as this endpoint takes it, which is a copy of the body, is all that remains charged: held, which the
// attempt gives back once the request is sent. A refusal is ErrOverloaded. The compact call is the one request that is
// not a generation: its body is rewritten by the members that must not go upstream as they are (the endpoint's model
// name, the identifiers, the routing parameters) and nothing else (protocol's Target.Compact).
func (s *session) prepare(entry routing.Entry) (up protocol.Upstream, held int64, err error) {
	w := protocol.Rewrites{Identity: s.x.identity(uuid.UUID(s.c.caller.AccountID()))}
	target := entry.Target
	target.Compact = s.c.Compact
	cost := protocol.PrepareCost(&s.c.Request, s.c.Body, target, w)
	if !s.x.Memory.Charge(cost) {
		return up, 0, ErrOverloaded
	}
	up, err = protocol.Prepare(&s.c.Request, s.c.Body, target, w)
	held = min(cost, int64(cap(up.Body)))
	if err != nil {
		held = 0
	}
	s.x.Memory.Refund(cost - held)
	return up, held, err
}

// unprepared is the end of a call whose request cannot be made ready for an endpoint. A request the client got wrong
// is the client's; anything else is ours.
func (s *session) unprepared(err error) ended {
	if errors.Is(err, protocol.ErrInvalid) {
		return ended{err: protocol.Error{Type: protocol.InvalidRequest, Message: strings.TrimPrefix(err.Error(), protocol.ErrInvalid.Error()+": ")}}
	}
	s.logger.Error("a request could not be prepared for an endpoint", "err", err)
	return ended{err: protocol.Error{Type: protocol.Overloaded}, after: 1}
}

// exclude writes an entry that was passed over into the route's record.
func (s *session) exclude(e routing.Entry, why routing.Reason) {
	s.c.Plan.Record.Excluded = append(s.c.Plan.Record.Excluded, routing.Excluded{Endpoint: e.Endpoint, Reason: why})
}

// record writes how an attempt went into the route's record.
func (s *session) record(a *attempt, failed bool, scope routing.Scope, next routing.Action) {
	s.c.Plan.Record.Attempts = append(s.c.Plan.Record.Attempts, routing.Attempt{
		Endpoint: a.entry.Endpoint, Failed: failed, Scope: scope, Action: next,
	})
}

// attempt is one try at an endpoint, from the moment its identity is fixed. The goroutine that runs the call owns it.
type attempt struct {
	s       *session
	i       int // index in s.ends
	entry   routing.Entry
	conn    Conn
	meter   *billing.Meter // bound to the entry that was priced
	cancel  context.CancelCauseFunc
	release context.CancelFunc
	unhook  func() bool // of the node's shutdown, which cancels the attempt

	content bool              // the upstream sent content: the commit point came
	usage   protocol.Usage    // what the stream reported, once it is over
	seen    protocol.Progress // what the relay had seen last

	asked   billing.ExtendRequest
	landing chan error // the outcome of the extension asked for, nil when none is in flight
}

// bind fixes the attempt's identity before anything is sent: its number, and the entry whose price the reservation
// holds, which Bind refuses to hand to an entry the reservation dropped (I1). From here the attempt counts, whatever
// becomes of it.
func (s *session) bind(ctx context.Context, i int, entry routing.Entry, conn Conn) (*attempt, context.Context) {
	kept := s.c.Hold.Kept[i]
	a := &attempt{s: s, i: len(s.ends), entry: entry, conn: conn, meter: s.c.Hold.Bind(kept)}
	s.ends = append(s.ends, billing.AttemptResult{Seq: len(s.ends) + 1, Entry: kept, Result: billing.Unknown})
	s.tries = append(s.tries, try{entry: entry, started: time.Now()})
	s.currency = entry.Price.Schedule.Currency()
	s.last = a
	policy := conn.Policy()
	ctx, release := AttemptContext(ctx, s.deadline, policy.Cancels)
	ctx, a.cancel = context.WithCancelCause(ctx)
	a.release = release
	a.unhook = context.AfterFunc(s.x.shutdown, func() { a.cancel(errShutdown) })
	s.x.Router.Begin(entry.Endpoint)
	s.setCancel(a.cancel, policy.Cancels)
	return a, ctx
}

// close ends the attempt: its upstream is cancelled if it still runs, the credential's slot and the endpoint's count
// are given back, and what billing takes of it is made from its usage and the receipt, which has the supplier's
// request id.
func (a *attempt) close() {
	a.s.setCancel(nil, false)
	a.unhook()
	a.cancel(nil)
	a.release()
	a.result(a.conn.Done())
	a.s.x.Router.End(a.entry.Endpoint)
}

// attempt runs one try: send, and either a failure to decide on or an answer to read, a stream for a client relayed, a
// whole body for one that is passed through.
func (s *session) attempt(ctx context.Context, i int, entry routing.Entry, conn Conn, up protocol.Upstream, held int64) (step, ended) {
	s.let() // another attempt answers the call: the reply kept before it is not part of it
	a, ctx := s.bind(ctx, i, entry, conn)
	defer a.close()
	resp, fail := a.send(ctx, up, held)
	if fail != nil {
		return s.failed(a, fail)
	}
	defer resp.Body.Close() //nolint:errcheck // the stream is over or abandoned; nothing to learn from the close
	if s.c.Compact {
		return a.pass(ctx, resp)
	}
	return a.stream(ctx, resp.Body, up.Usage)
}

// send posts the request, within the wait for the response headers, which is when the supplier's request id comes. The
// prepared body, held in the node's budget, is given back as soon as it has been sent. The compact endpoint is the one
// whose reply is not a stream: it gets a longer wait for its headers, and its own path (docs/domains/gateway.md
// 「路径与格式」).
func (a *attempt) send(ctx context.Context, up protocol.Upstream, held int64) (*http.Response, *suppliers.Failure) {
	s := a.s
	defer s.x.Memory.Refund(held)
	waitFor := s.x.Limits.cfg.HeaderWait
	call := suppliers.Call{Format: a.entry.Target.Format, Body: up.Body, Header: s.c.Header}
	if s.c.Compact {
		waitFor, call.Compact = s.x.Limits.cfg.CompactHeaderWait, true
	}
	wait := time.AfterFunc(waitFor, func() { a.cancel(errHeaderWait) })
	resp, fail := a.conn.Send(ctx, call)
	wait.Stop() // at once: a late firing would cancel the stream the headers began
	return resp, fail
}

// failed decides what to do after an attempt the supplier answered with a failure, or did not answer: whether the
// request ran is the adapter's word (NotExecuted) or unknown, and routing says whether to try another credential, the
// next entry, or give the client the error.
func (s *session) failed(a *attempt, f *suppliers.Failure) (step, ended) {
	s.ends[a.i].Result = billing.Unknown
	if s.isGone() { // the client left while the request was out: the failure is the cancel's, and cancelling is never free
		s.ends[a.i].Finish = billing.FinishCancelled
	}
	if f.Executed == suppliers.NotExecuted {
		s.ends[a.i].Result = billing.NotExecuted
	}
	scope := routingScope(f.Scope)
	limit, limited := f.Cause.(suppliers.RateLimited)
	if limited || scope == routing.ScopeSupplier {
		s.x.Router.Block(a.entry, scope, limit.RetryAfter)
	}
	if limited {
		s.x.Router.Throttled(a.entry.Endpoint)
	}
	if scope == routing.ScopeSupplier {
		s.skips.failedSuppliers = append(s.skips.failedSuppliers, a.entry.Supplier)
	}
	next := routing.Decide(routing.Failure{Scope: scope})
	s.record(a, true, scope, next)
	switch next {
	case routing.ReturnToClient: // the request itself is refused: the client gets what the supplier said of it, cleaned of its name
		return over, ended{err: protocol.Error{Type: protocol.InvalidRequest, Rejection: f.Rejection}}
	case routing.OtherCredential:
		return sameEntry, ended{}
	case routing.NextEntry:
	}
	return nextEntry, ended{}
}

// routingScope is the scope routing decides by for the scope an adapter classified a failure with. The two types list
// the same scopes, but each in its own module's order, so they are matched by name rather than by number.
func routingScope(s suppliers.Scope) routing.Scope {
	switch s {
	case suppliers.ScopeRequest:
		return routing.ScopeRequest
	case suppliers.ScopeEgress:
		return routing.ScopeEgress
	case suppliers.ScopeEndpoint:
		return routing.ScopeEndpoint
	case suppliers.ScopeCredential:
		return routing.ScopeCredential
	case suppliers.ScopeGroup:
		return routing.ScopeGroup
	case suppliers.ScopeSupplier:
		return routing.ScopeSupplier
	}
	panic("gateway: a failure scope routing does not have")
}
