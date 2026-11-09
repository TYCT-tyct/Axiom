package gateway

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"axiom/internal/billing"
	"axiom/internal/catalog"
	"axiom/internal/protocol"
	"axiom/internal/quality"
	"axiom/internal/routing"
)

// servedModel is the public name of the model the attempt's endpoint serves, so a models fallback shows which model
// answered. The route snapshot freezes it, so a snapshot newer than the one the request read cannot make the answer name
// a different model; the internal @version suffix is never a public name.
func (s *session) servedModel(a *attempt) string {
	if a.entry.ModelSlug != "" {
		return catalog.Normalize(strings.SplitN(a.entry.ModelSlug, "@", 2)[0])
	}
	if s.c.snap != nil {
		if m, ok := s.c.snap.Model(a.entry.Model); ok {
			return catalog.Normalize(strings.SplitN(m.Slug, "@", 2)[0])
		}
	}
	return s.c.Request.Model
}

// stream relays the answer of an attempt whose response headers came: the relay holds the upstream's first events until
// the first content, the commit point (I4), and from there each step is written to the client as it comes. The
// goroutine that calls it is the relay's owner: it alone calls the relay, one read at a time. Only the idle timer
// beside it acts on the attempt, by cancelling it, which closes the body and ends the read.
func (a *attempt) stream(ctx context.Context, body io.ReadCloser, uc protocol.UsageContext) (step, ended) {
	s := a.s
	idleFor := s.x.Limits.cfg.IdleTimeout
	defer a.landed(true) //nolint:errcheck // see the extension's goroutine
	defer s.setWriteFor(s.x.Limits.cfg.WriteTimeout)
	if s.x.Cache != nil && s.c.cache.enabled {
		if capture := newCacheCapture(s, body); capture != nil {
			a.capture, body = capture, capture
		}
	}
	for {
		relay := protocol.NewRelay(s.c.Format, a.entry.Target.Format, body, uc, protocol.RelayOptions{
			ID: s.c.ID.String(), Model: s.c.Request.Model, ActualModel: s.servedModel(a), Created: s.start.Unix(),
			Memory: s.x.Memory, Aggregate: s.aggregate, Citations: s.citations, Echo: s.echo,
			ZeroOutput: s.c.Request.ZeroOutput(), ServerTool: a.serverTool(),
		})
		s.live = relay
		idle := time.AfterFunc(idleFor, func() { a.cancel(errIdle) })
		st, e, next := a.readHop(ctx, relay, idle, idleFor)
		idle.Stop()
		body.Close() //nolint:errcheck,gosec // the hop is over or abandoned; the close tells us nothing
		if !next {
			return st, e
		}
		body, uc = a.nextBody, a.nextUsage
	}
}

// next reads the relay's next step, dating the upstream's first content and its end or failure as they are read: the
// meter may then wait for an extension, which is the gateway's time, not the upstream's (quality's measurements).
func (a *attempt) next(relay *protocol.Relay) (protocol.Step, error) {
	st, err := relay.Next()
	switch {
	case err != nil, st.Kind == protocol.StepUpstreamDone:
		a.obs.Ended = time.Now()
	case st.Kind == protocol.StepCommit:
		a.obs.FirstToken = time.Now()
		if t := &a.s.tries[a.i]; t.firstToken.IsZero() { // a loop's later hops are samplings of their own
			t.firstToken = a.obs.FirstToken
		}
	}
	return st, err
}

// commit is the first content: from here the status is 200 and nothing is retried (I4). A client that is gone gets
// nothing, and the attempt, if it goes on, is not committed. A client that wants no stream is written to once, at the
// end: its status is not fixed here, so an attempt that fails may still be given up on (I4).
func (s *session) commit(a *attempt, out []byte) {
	a.content = true
	s.state = a.state // the attempt answers the call: its upstream's state is the one that goes with the answer
	if s.isGone() {
		return
	}
	a.obs.Delivered = true
	s.beginReply(out)
}

// broke handles a read that failed. What it was depends on why the attempt was cancelled, if it was: the client
// leaving, the upstream idle, the call out of time; otherwise the stream itself broke. Before the commit point the
// attempt is lost but the call goes on with the next entry, unless the call itself is out of road; after it the stream
// is ended where it is.
func (a *attempt) broke(ctx context.Context, relay *protocol.Relay, err error) (step, ended) {
	s := a.s
	a.obs.Cut, a.obs.Fault = cutOf(ctx), protocol.FaultOf(err)
	a.toolCounts(relay)
	if failed, ok := errors.AsType[*extendFailed](context.Cause(ctx)); ok {
		return a.truncate(relay, cut(failed.err))
	}
	e, finish, again := s.classify(ctx, err, a.conn)
	s.failAttempt(a, e.Type.String())
	rec := &s.ends[a.i]
	if s.loop != nil {
		a.hopDone(relay)
		s.dropOut()
	}
	a.usage, rec.Finish = relay.Usage(), finish
	scope := scopeOf(a.obs.Fault)
	if s.aggregate && s.loop != nil {
		// A loop's sampling is the answer only once it has ended without a server call: one that broke off may be a tool
		// call the gateway never ran, and what it holds must not reach the client as a reply to charge.
		rec.Result = billing.FailedBeforeCommit
		s.drop(relay)
		return s.unanswered(a, e, again, scope)
	}
	if s.aggregate {
		return s.brokeAggregate(a, relay, e, again, scope)
	}
	if s.isStarted() {
		rec.Result = billing.Committed
		s.record(a, true, scope, routing.ReturnToClient)
		return over, ended{relay: relay, err: e}
	}
	rec.Result = billing.FailedBeforeCommit
	s.drop(relay)
	return s.unanswered(a, e, again, scope)
}

// classify says how a failed attempt ended: the error the client is answered with, the finish reason, and whether
// another entry may be tried. A relay that broke and a whole reply that could not be read fail for the same reasons
// (docs/domains/gateway.md「错误」).
func (s *session) classify(ctx context.Context, err error, conn Conn) (protocol.Error, billing.FinishReason, bool) {
	e := protocol.Error{Type: protocol.UpstreamFailed}
	finish, again := billing.FinishError, true
	cause, fault := context.Cause(ctx), protocol.FaultOf(err)
	switch {
	case s.isGone():
		finish, again = billing.FinishCancelled, false
	case errors.Is(cause, context.DeadlineExceeded), errors.Is(cause, errShutdown):
		e, finish, again = protocol.Error{Type: protocol.Timeout}, billing.FinishTimeout, false
	case errors.Is(err, protocol.ErrMemory):
		e, again = protocol.Error{Type: protocol.Overloaded}, false
	case errors.Is(cause, errIdle):
		e, finish = protocol.Error{Type: protocol.Timeout}, billing.FinishTimeout
	case upstreamRefused(fault):
		again = false
		typ := protocol.InvalidRequest
		switch fault {
		case protocol.FaultContextTooLong:
			typ = protocol.ContextLengthExceeded
		case protocol.FaultModeration:
			typ = protocol.ContentPolicyViolation
		case protocol.FaultNone, protocol.FaultBroken, protocol.FaultServer, protocol.FaultRateLimited,
			protocol.FaultBadRequest, protocol.FaultUnrecognised, protocol.FaultToolInput:
		}
		r := protocol.RejectionOf(err)
		e = requestError(typ, r)
		if r != nil {
			e.Rejection = conn.Sanitize(*r)
		}
	}
	return e, finish, again
}

// toolCounts takes the answer's tool-call checks from the relay as it ends, normally or not: a call whose input the
// client's format cannot take fails the read after the relay counted it (docs/domains/quality.md「线上观测」).
func (a *attempt) toolCounts(relay *protocol.Relay) {
	p := relay.Progress()
	a.obs.ToolChecks, a.obs.ToolErrors = p.ToolChecks, p.ToolErrors
}

// upstreamRefused reports whether the fault is the upstream saying the request itself is wrong: its context, the
// content or the request's form. Those are the classes no other endpoint can serve, and the two a client can act on
// (docs/domains/gateway.md「错误」).
func upstreamRefused(fault protocol.Fault) bool {
	switch fault {
	case protocol.FaultBadRequest, protocol.FaultContextTooLong, protocol.FaultModeration:
		return true
	case protocol.FaultNone, protocol.FaultBroken, protocol.FaultServer, protocol.FaultRateLimited,
		protocol.FaultUnrecognised, protocol.FaultToolInput:
	}
	return false
}

// scopeOf is whose failure a failed attempt is recorded as: a request the upstream refused is the request's, as the
// same answer over HTTP is, and everything else is the endpoint's (docs/domains/gateway.md「状态码」).
func scopeOf(fault protocol.Fault) routing.Scope {
	if upstreamRefused(fault) {
		return routing.ScopeRequest
	}
	return routing.ScopeEndpoint
}

// unanswered ends an attempt that has nothing for the client: one that may still be given up on moves to the next
// entry; one that may not answers with the error, unless the client is gone, which gets nothing. scope is whose the
// failure is, which the call's record keeps as the supplier's own failures are kept.
func (s *session) unanswered(a *attempt, e protocol.Error, again bool, scope routing.Scope) (step, ended) {
	if again && s.searched {
		// A search already ran on this call's account. Another entry would have to run it again, and the client was
		// promised one stream, so the failure ends the loop as an upstream error.
		e, again = protocol.Error{Type: protocol.UpstreamFailed}, false
	}
	if !again {
		s.record(a, true, scope, routing.ReturnToClient)
		if s.isGone() {
			return over, ended{}
		}
		return over, ended{err: e}
	}
	s.record(a, true, scope, routing.NextEntry)
	return nextEntry, ended{}
}

// brokeAggregate ends an attempt of a call that did not stream: the client has nothing yet, so an attempt that
// generated content may still be given up on for another entry, and what it generated answers the call if no entry is
// left (docs/domains/gateway.md「转发规则」). A request refusal returns the partial reply without another attempt; local
// cancellation, deadlines and memory failures discard it.
func (s *session) brokeAggregate(a *attempt, relay *protocol.Relay, e protocol.Error, again bool, scope routing.Scope) (step, ended) {
	rec := &s.ends[a.i]
	rec.Result = billing.FailedBeforeCommit
	if !again && (!a.content || !requestRefusals[e.Type]) {
		s.drop(relay)
		return s.unanswered(a, e, false, scope)
	}
	if a.content {
		s.keep(relay) // it answers the call if no entry is left, and nothing else of it reaches the client
	} else {
		s.drop(relay)
	}
	next, answer := s.unanswered(a, e, again, scope)
	if !again {
		answer = s.left(answer)
	}
	return next, answer
}

// keep holds the reply of an aggregate attempt that generated content: the answer when no entry is left. The reply kept
// before it goes, so that only the last attempt's content is returned, never two (docs/domains/gateway.md「转发规则」).
func (s *session) keep(relay *protocol.Relay) {
	s.let()
	s.part = relay
}

// left ends a call that has no entry left: an aggregate attempt that generated content answers with it, as the last
// attempt that failed with content is answered with what it made (docs/domains/gateway.md「错误」). A client that has
// gone gets nothing, and what was kept is let go: nothing of it reached it.
func (s *session) left(e ended) ended {
	if s.part == nil || e.relay != nil {
		return e
	}
	if s.isGone() {
		s.let()
		return e
	}
	e.relay, s.part = s.part, nil
	if s.c.Format != protocol.Messages { // Messages has no stop reason for an error: it answers the error alone
		s.ends[len(s.ends)-1].Result = billing.Aggregated
	}
	return e
}

// complete handles the upstream's normal end. After the commit point the stream is done and the call settles before it
// ends. Before it, there was no content: a 200 with nothing in it is a failure and the next entry is tried, unless the
// client is gone, whose upstream was read to the end for the usage it will be billed. A call that did not stream has
// its whole reply at its end, and answers with it.
func (a *attempt) complete(relay *protocol.Relay) (step, ended) {
	s := a.s
	a.usage = relay.Usage()
	a.obs.Done, a.obs.Empty = true, !a.content
	a.toolCounts(relay)
	if s.aggregate {
		return a.completeAggregate(relay)
	}
	rec := &s.ends[a.i]
	if s.isStarted() {
		rec.Result, rec.Finish = billing.Committed, billing.FinishStop
		if s.isGone() {
			rec.Finish = billing.FinishCancelled
		}
		return s.answered(a, relay)
	}
	rec.Result = billing.FailedBeforeCommit
	s.drop(relay)
	if s.isGone() {
		rec.Finish = billing.FinishCancelled
		s.record(a, true, routing.ScopeEndpoint, routing.ReturnToClient)
		return over, ended{}
	}
	return s.empty(a)
}

// completeAggregate is complete for a call that did not stream: the whole reply answers it, unless the client is gone
// or the reply is empty.
func (a *attempt) completeAggregate(relay *protocol.Relay) (step, ended) {
	s := a.s
	rec := &s.ends[a.i]
	rec.Finish = billing.FinishStop
	switch {
	case s.isGone():
		rec.Result, rec.Finish = billing.FailedBeforeCommit, billing.FinishCancelled
		s.drop(relay)
		s.record(a, true, routing.ScopeEndpoint, routing.ReturnToClient)
		return over, ended{}
	case !a.content:
		rec.Result = billing.FailedBeforeCommit
		s.drop(relay)
		return s.empty(a)
	}
	rec.Result = billing.Aggregated
	return s.answered(a, relay)
}

// answered ends the call with the attempt whose upstream ended normally: its reply is offered to the cache once the
// call settles.
func (s *session) answered(a *attempt, relay *protocol.Relay) (step, ended) {
	s.capture(a, a.capture)
	s.record(a, false, 0, 0)
	return over, ended{relay: relay, done: true}
}

// empty ends an attempt whose upstream answered 200 with nothing in it: a failure, which the next entry answers unless
// a search already ran.
func (s *session) empty(a *attempt) (step, ended) {
	s.failAttempt(a, protocol.UpstreamFailed.String())
	if s.searched {
		s.record(a, true, routing.ScopeEndpoint, routing.ReturnToClient)
		return over, ended{err: protocol.Error{Type: protocol.UpstreamFailed}}
	}
	s.record(a, true, routing.ScopeEndpoint, routing.NextEntry)
	return nextEntry, ended{}
}

// truncate cuts the stream short where its reservation could not be extended: the upstream is stopped at once, and
// the call ends with the billing error, the stream in place after the commit point, or an answer before it, when
// nothing reached the client and so no model usage is charged (docs/domains/billing.md「免单规则」). No other entry is
// tried: it would draw on the same account.
func (a *attempt) truncate(relay *protocol.Relay, e ended) (step, ended) {
	s := a.s
	a.cancel(errCut)
	a.obs.Cut = quality.Ours
	s.failAttempt(a, e.err.Type.String())
	rec := &s.ends[a.i]
	if s.loop != nil {
		a.hopDone(relay)
		s.dropOut()
	}
	a.usage, rec.Finish = relay.Usage(), billing.FinishTruncated
	s.record(a, true, routing.ScopeEndpoint, routing.ReturnToClient)
	if s.isStarted() {
		rec.Result = billing.Committed
		e.relay = relay
		return over, e
	}
	rec.Result = billing.FailedBeforeCommit
	s.drop(relay)
	if s.isGone() {
		return over, ended{}
	}
	return over, e
}

// drop lets go of a relay whose stream is not going to be used: its ending is written for nobody, which gives back
// what it held of the node's memory, and the session lets go of the relay itself. A panic before that leaves it in live.
func (s *session) drop(relay *protocol.Relay) {
	relay.Fail(protocol.Error{Type: protocol.UpstreamFailed}, nil)
	relay.Release() // an aggregate relay keeps its budget past Fail for a body that will not be written
	if s.live == relay {
		s.live = nil
	}
}
