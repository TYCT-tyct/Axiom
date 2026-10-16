package gateway

import (
	"context"
	"errors"
	"net/http"
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
)

// skips is why entries were passed over without an attempt (internal/gateway/README.md「尝试」).
type skips struct {
	noSlot, noEndpoint bool
	unsupported        string // the first reason an endpoint could not express the request
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
			return e
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
	return s.exhausted()
}

// mayStart says whether another attempt may begin, and if not how the call ends. A client that has gone gets nothing,
// a holder that may be gone, or a call at the end of its time, no new attempt (the reservation may no longer cover it),
// and a call past StartBy none either.
func (s *session) mayStart(maxAttempts int) (ended, bool) {
	switch {
	case s.req.Context().Err() != nil:
		return ended{}, true
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
	conn, err := s.x.Upstreams.Acquire(uuid.UUID(entry.Endpoint))
	if err != nil {
		s.skips.noSlot = s.skips.noSlot || errors.Is(err, suppliers.ErrNoSlot)
		s.skips.noEndpoint = s.skips.noEndpoint || !errors.Is(err, suppliers.ErrNoSlot)
		s.exclude(entry, routing.Full)
		return nextEntry, ended{}
	}
	up, err := protocol.Prepare(&s.c.Request, s.c.Body, entry.Target, protocol.Rewrites{Identity: s.x.identity(uuid.UUID(s.c.caller.AccountID()))})
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
		s.exclude(entry, routing.RetryBudget)
		return nextEntry, ended{}
	}
	if !s.x.Memory.Charge(int64(len(up.Body))) { // the request as this endpoint takes it is a copy of the body
		conn.Done()
		return over, ended{err: protocol.Error{Type: protocol.Overloaded}, after: 1}
	}
	return s.attempt(ctx, i, entry, conn, up)
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

// attempt is one try at an endpoint, from the moment its identity is fixed.
type attempt struct {
	s       *session
	i       int // index in s.ends
	entry   routing.Entry
	conn    Conn
	meter   *billing.Meter // bound to the entry that was priced; metering reads it (G6)
	cancel  context.CancelCauseFunc
	release context.CancelFunc
}

// bind fixes the attempt's identity before anything is sent: its number, and the entry whose price the reservation
// holds, which Bind refuses to hand to an entry the reservation dropped (I1). From here the attempt counts, whatever
// becomes of it.
func (s *session) bind(ctx context.Context, i int, entry routing.Entry, conn Conn) (*attempt, context.Context) {
	kept := s.c.Hold.Kept[i]
	a := &attempt{s: s, i: len(s.ends), entry: entry, conn: conn, meter: s.c.Hold.Bind(kept)}
	s.ends = append(s.ends, AttemptEnd{Seq: len(s.ends) + 1, Entry: kept, Result: billing.Unknown})
	policy := conn.Policy()
	ctx, release := AttemptContext(ctx, s.deadline, policy.Cancels)
	ctx, a.cancel = context.WithCancelCause(ctx)
	a.release = release
	s.x.Router.Begin(entry.Endpoint)
	s.setCancel(a.cancel, policy.Cancels)
	s.journal(a, false)
	return a, ctx
}

// close ends the attempt: its upstream is cancelled if it still runs, the credential's slot and the endpoint's count
// are given back, and the receipt, which has the supplier's request id, is kept for the settlement.
func (a *attempt) close() {
	a.s.setCancel(nil, false)
	a.cancel(nil)
	a.release()
	a.s.ends[a.i].Receipt = a.conn.Done()
	a.s.x.Router.End(a.entry.Endpoint)
}

// journal appends the attempt to the reconciliation journal: when it is bound, when the supplier's request id came,
// and when its content was first handed to the client.
func (s *session) journal(a *attempt, committed bool) {
	site, account := uuid.UUID(s.c.site.ID), uuid.UUID(s.c.caller.AccountID())
	s.x.Upstreams.Journal(suppliers.JournalEntry{
		Request: platform.RequestID(s.c.ID), Seq: platform.AttemptSeq(s.ends[a.i].Seq), //nolint:gosec // at most platform.MaxAttempts
		Site: &site, Account: &account,
		Receipt: a.conn.Receipt(), Committed: committed,
	})
}

// attempt runs one try: send, and either a failure to decide on or a stream to relay.
func (s *session) attempt(ctx context.Context, i int, entry routing.Entry, conn Conn, up protocol.Upstream) (step, ended) {
	a, ctx := s.bind(ctx, i, entry, conn)
	defer a.close()
	resp, fail := a.send(ctx, up)
	if fail != nil {
		return s.failed(a, fail)
	}
	defer resp.Body.Close() //nolint:errcheck // the stream is over or abandoned; nothing to learn from the close
	return a.stream(ctx, resp.Body, up.Usage)
}

// send posts the request, within the wait for the response headers, and journals the attempt again when they come: that
// is when the supplier's request id is known. The prepared body is given back to the node's budget as soon as it has
// been sent.
func (a *attempt) send(ctx context.Context, up protocol.Upstream) (*http.Response, *suppliers.Failure) {
	s := a.s
	defer s.x.Memory.Refund(int64(len(up.Body)))
	wait := time.AfterFunc(s.x.Limits.cfg.HeaderWait, func() { a.cancel(errHeaderWait) })
	resp, fail := a.conn.Send(ctx, suppliers.Call{Format: a.entry.Target.Format, Body: up.Body, Header: s.c.Header})
	wait.Stop() // at once: a late firing would cancel the stream the headers began
	s.journal(a, false)
	return resp, fail
}

// failed decides what to do after an attempt the supplier answered with a failure, or did not answer: whether the
// request ran is the adapter's word (NotExecuted) or unknown, and routing says whether to try another credential, the
// next entry, or give the client the error.
func (s *session) failed(a *attempt, f *suppliers.Failure) (step, ended) {
	s.ends[a.i].Result = billing.Unknown
	if f.Executed == suppliers.NotExecuted {
		s.ends[a.i].Result = billing.NotExecuted
	}
	if _, ok := f.Cause.(suppliers.RateLimited); ok {
		s.x.Router.Throttled(a.entry.Endpoint)
	}
	scope := routing.Scope(f.Scope)
	next := routing.Decide(routing.Failure{Scope: scope})
	s.record(a, true, scope, next)
	switch next {
	case routing.ReturnToClient: // the supplier's own words are not kept in a Failure, so the client gets ours
		return over, ended{err: protocol.Error{Type: protocol.InvalidRequest}}
	case routing.OtherCredential:
		return sameEntry, ended{}
	case routing.NextEntry:
	}
	return nextEntry, ended{}
}
