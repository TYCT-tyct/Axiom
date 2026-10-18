package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"axiom/internal/billing"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// stream relays the answer of an attempt whose response headers came: the relay holds the upstream's first events until
// the first content, the commit point (I4), and from there each step is written to the client as it comes. The
// goroutine that calls it is the relay's owner: it alone calls the relay, one read at a time. Only the idle timer
// beside it acts on the attempt, by cancelling it, which closes the body and ends the read.
func (a *attempt) stream(ctx context.Context, body io.Reader, uc protocol.UsageContext) (step, ended) {
	s := a.s
	relay := protocol.NewRelay(s.c.Format, a.entry.Target.Format, body, uc, protocol.RelayOptions{
		ID: s.c.ID.String(), Model: s.c.Request.Model, Created: s.start.Unix(), Memory: s.x.Memory, Aggregate: s.aggregate,
	})
	idleFor := s.x.Limits.cfg.IdleTimeout
	idle := time.AfterFunc(idleFor, func() { a.cancel(errIdle) })
	defer idle.Stop()
	// Billing settles with no extension in flight, and the extension's goroutine must not outlive the call, whose slots
	// bound it; its outcome changes nothing now.
	defer a.landed(true) //nolint:errcheck // see above
	// Once the upstream is no longer read, a write that blocks lets it generate nothing more on the call's account.
	defer s.setWriteFor(s.x.Limits.cfg.WriteTimeout)
	for {
		st, err := relay.Next()
		if err != nil {
			return a.broke(ctx, relay, err)
		}
		// The upstream is idle when it sends nothing new: a keep-alive, a comment and a repeated snapshot do not
		// count, bytes that make progress on an event and content do (docs/domains/gateway.md「路径与格式」).
		p := relay.Progress()
		if st.Kind == protocol.StepCommit || p.ContentBytes != a.seen.ContentBytes ||
			p.PartialBytes > a.seen.PartialBytes || p.Searches != a.seen.Searches {
			idle.Reset(idleFor)
		}
		a.seen = p
		// Every read is metered before what it brought is written, before the commit point too (I1).
		if err := a.metered(ctx, p, st.Kind == protocol.StepUpstreamDone, idle, idleFor); err != nil {
			return a.truncate(relay, cut(err))
		}
		switch st.Kind {
		case protocol.StepCommit:
			s.commit(a, st.Out)
		case protocol.StepData, protocol.StepHeartbeat:
			s.write(st.Out)
		case protocol.StepUpstreamDone:
			return a.complete(relay)
		case protocol.StepProgress, protocol.StepBuffered:
		}
	}
}

// commit is the first content: from here the status is 200 and nothing is retried (I4). A client that is gone gets
// nothing, and the attempt, if it goes on, is not committed. A client that wants no stream is written to once, at the
// end: its status is not fixed here, so an attempt that fails may still be given up on (I4).
func (s *session) commit(a *attempt, out []byte) {
	a.content = true
	if s.isGone() {
		return
	}
	if s.aggregate {
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	s.w.WriteHeader(http.StatusOK)
	if !s.put(out) {
		return
	}
	s.started = true
	s.keepAlive = time.AfterFunc(s.x.Limits.cfg.KeepAlive, s.sendKeepAlive)
}

// write sends a step's events to a client the stream began for.
func (s *session) write(b []byte) {
	if len(b) == 0 {
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.started && !s.closed {
		s.put(b)
	}
}

// put writes b and flushes it, within one write's deadline.
func (s *session) put(b []byte) bool { return s.putFor(b, s.writeFor) }

// putFor writes b and flushes it within d. A client whose context has ended, or a write to it that fails, is gone:
// nothing more is written to it, and an attempt that stops when the client does is stopped now. wmu is held.
func (s *session) putFor(b []byte, d time.Duration) bool {
	if s.gone || s.req.Context().Err() != nil {
		s.gone = true
		return false
	}
	_ = s.rc.SetWriteDeadline(time.Now().Add(d)) //nolint:errcheck // a writer without deadlines is written to as it is
	_, err := s.w.Write(b)                       //nolint:gosec // an API response, which is never rendered as a page
	if err == nil {
		err = s.rc.Flush()
	}
	if err != nil {
		s.gone = true
		if s.follows && s.cancel != nil {
			s.cancel(errClientGone)
		}
		return false
	}
	if s.keepAlive != nil && !s.closed {
		s.keepAlive.Reset(s.x.Limits.cfg.KeepAlive)
	}
	return true
}

// sendKeepAlive writes the format's own keep-alive to a stream that has been quiet; every write moves the timer on. The
// idle timeout, not this, ends a stream whose upstream has stopped.
func (s *session) sendKeepAlive() {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if !s.closed {
		s.put(protocol.KeepAlive(s.c.Format))
	}
}

// finish writes the end of the stream, if the client is still there, and stops the keep-alive: nothing is written
// after it.
func (s *session) finish(out []byte) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.closed = true
	if s.keepAlive != nil {
		s.keepAlive.Stop()
	}
	if s.started && len(out) > 0 {
		s.put(out)
	}
}

// respond writes a whole body to a client that did not stream: it is the call's only answer, so the status is set here.
// A body larger than one write goes out in chunks, each within the sooner of one write's deadline and what is left of
// the reply's own, so that a client that reads slowly but keeps reading is not taken for one that has gone, while a
// client too slow to be served in the reply's time is (docs/domains/gateway.md「转发规则」, README.md「超时与限额」).
func (s *session) respond(status int, body []byte) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.closed = true
	if s.keepAlive != nil {
		s.keepAlive.Stop()
	}
	if s.started || s.gone || s.req.Context().Err() != nil {
		return
	}
	s.started = true
	h := s.w.Header()
	h.Set("Content-Type", "application/json")
	s.w.WriteHeader(status)
	until := time.Now().Add(max(s.writeFor, time.Duration(int64(len(body))*int64(time.Second)/respondRate)))
	for b := body; len(b) > 0; {
		n := min(len(b), respondChunk)
		left := time.Until(until)
		if left <= 0 {
			s.gone = true // too slow to be served in the reply's time: taken for a client that has gone
			return
		}
		if !s.putFor(b[:n], min(s.writeFor, left)) {
			return
		}
		b = b[n:]
	}
}

const (
	// respondChunk is how much of a whole reply one write takes.
	respondChunk = 64 << 10
	// respondRate is the slowest reader a whole reply is written for, in bytes a second: the reply as a whole gets the
	// longer of one write's deadline and its bytes at this rate (16 MiB take 256s), so a client can neither be cut off
	// by a slow link nor hold a slot and the node's memory for longer than that.
	respondRate = 64 << 10
)

// answer writes the error a call that forwarded nothing ends with, in the client's own format, with what was charged
// anyway when the settlement says so.
func (s *session) answer(e ended, fin *protocol.Final) {
	status, body := protocol.RenderError(s.c.Format, e.err, fin)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.gone || s.started || s.req.Context().Err() != nil {
		return
	}
	h := s.w.Header()
	h.Set("Content-Type", "application/json")
	if e.after > 0 {
		h.Set("Retry-After", strconv.Itoa(min(e.after, maxRetryAfter)))
	}
	s.w.WriteHeader(status)
	s.put(body)
}

// setWriteFor sets the deadline of the writes from now on.
func (s *session) setWriteFor(d time.Duration) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.writeFor = d
}

// setCancel records how to stop the attempt in progress, for a write that fails.
func (s *session) setCancel(cancel context.CancelCauseFunc, follows bool) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.cancel, s.follows = cancel, follows
}

// isGone reports whether the client has left: its context ended, or a write to it failed.
func (s *session) isGone() bool {
	if s.req.Context().Err() != nil {
		return true
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.gone
}

func (s *session) isStarted() bool {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.started
}

// broke handles a read that failed. What it was depends on why the attempt was cancelled, if it was: the client
// leaving, the upstream idle, the call out of time; otherwise the stream itself broke. Before the commit point the
// attempt is lost but the call goes on with the next entry, unless the call itself is out of road; after it the stream
// is ended where it is.
func (a *attempt) broke(ctx context.Context, relay *protocol.Relay, err error) (step, ended) {
	s := a.s
	if failed, ok := errors.AsType[*extendFailed](context.Cause(ctx)); ok {
		return a.truncate(relay, cut(failed.err))
	}
	e, finish, again := s.classify(ctx, err)
	rec := &s.ends[a.i]
	a.usage, rec.Finish = relay.Usage(), finish
	if s.aggregate {
		return s.brokeAggregate(a, relay, e, again)
	}
	if s.isStarted() {
		rec.Result = billing.Committed
		s.record(a, true, routing.ScopeEndpoint, routing.ReturnToClient)
		return over, ended{relay: relay, err: e}
	}
	rec.Result = billing.FailedBeforeCommit
	drop(relay)
	return s.unanswered(a, e, again)
}

// classify says how a failed attempt ended: the error the client is answered with, the finish reason, and whether
// another entry may be tried. A relay that broke and a whole reply that could not be read fail for the same reasons
// (docs/domains/gateway.md「错误」).
func (s *session) classify(ctx context.Context, err error) (protocol.Error, billing.FinishReason, bool) {
	e := protocol.Error{Type: protocol.UpstreamFailed}
	finish, again := billing.FinishError, true
	cause := context.Cause(ctx)
	switch {
	case s.isGone():
		finish, again = billing.FinishCancelled, false
	case errors.Is(cause, errIdle):
		e, finish = protocol.Error{Type: protocol.Timeout}, billing.FinishTimeout
	case errors.Is(cause, context.DeadlineExceeded), errors.Is(cause, errShutdown):
		e, finish, again = protocol.Error{Type: protocol.Timeout}, billing.FinishTimeout, false
	case errors.Is(err, protocol.ErrMemory):
		e, again = protocol.Error{Type: protocol.Overloaded}, false
	}
	return e, finish, again
}

// unanswered ends an attempt that has nothing for the client: one that may still be given up on moves to the next
// entry; one that may not answers with the error, unless the client is gone, which gets nothing.
func (s *session) unanswered(a *attempt, e protocol.Error, again bool) (step, ended) {
	if !again {
		s.record(a, true, routing.ScopeEndpoint, routing.ReturnToClient)
		if s.isGone() {
			return over, ended{}
		}
		return over, ended{err: e}
	}
	s.record(a, true, routing.ScopeEndpoint, routing.NextEntry)
	return nextEntry, ended{}
}

// brokeAggregate ends an attempt of a call that did not stream: the client has nothing yet, so an attempt that
// generated content may still be given up on for another entry, and what it generated answers the call if no entry is
// left (docs/domains/gateway.md「转发规则」). An attempt that cannot be retried answers with the error, without what it
// made: the reservation is what failed, or the call is out of road.
func (s *session) brokeAggregate(a *attempt, relay *protocol.Relay, e protocol.Error, again bool) (step, ended) {
	rec := &s.ends[a.i]
	rec.Result = billing.FailedBeforeCommit
	if !again { // the client is gone, the call is out of road or the reservation failed: the error alone answers it
		drop(relay)
		return s.unanswered(a, e, false)
	}
	if a.content {
		s.keep(relay) // it answers the call if no entry is left, and nothing else of it reaches the client
	} else {
		drop(relay)
	}
	return s.unanswered(a, e, true)
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
	rec := &s.ends[a.i]
	a.usage = relay.Usage()
	if s.aggregate {
		rec.Finish = billing.FinishStop
		switch {
		case s.isGone():
			rec.Result, rec.Finish = billing.FailedBeforeCommit, billing.FinishCancelled
			drop(relay)
			s.record(a, true, routing.ScopeEndpoint, routing.ReturnToClient)
			return over, ended{}
		case !a.content: // a 200 with nothing in it is a failure: the next entry answers
			rec.Result = billing.FailedBeforeCommit
			drop(relay)
			s.record(a, true, routing.ScopeEndpoint, routing.NextEntry)
			return nextEntry, ended{}
		}
		rec.Result = billing.Aggregated
		s.record(a, false, 0, 0)
		return over, ended{relay: relay, done: true}
	}
	if s.isStarted() {
		rec.Result, rec.Finish = billing.Committed, billing.FinishStop
		if s.isGone() {
			rec.Finish = billing.FinishCancelled
		}
		s.record(a, false, 0, 0)
		return over, ended{relay: relay, done: true}
	}
	rec.Result = billing.FailedBeforeCommit
	drop(relay)
	if s.isGone() {
		rec.Finish = billing.FinishCancelled
		s.record(a, true, routing.ScopeEndpoint, routing.ReturnToClient)
		return over, ended{}
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
	rec := &s.ends[a.i]
	a.usage, rec.Finish = relay.Usage(), billing.FinishTruncated
	s.record(a, true, routing.ScopeEndpoint, routing.ReturnToClient)
	if s.isStarted() {
		rec.Result = billing.Committed
		e.relay = relay
		return over, e
	}
	rec.Result = billing.FailedBeforeCommit
	drop(relay)
	if s.isGone() {
		return over, ended{}
	}
	return over, e
}

// drop lets go of a relay whose stream is not going to be used: its ending is written for nobody, which gives back
// what it held of the node's memory.
func drop(relay *protocol.Relay) { relay.Fail(protocol.Error{Type: protocol.UpstreamFailed}, nil) }
