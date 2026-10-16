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
		ID: s.c.ID.String(), Model: s.c.Request.Model, Created: s.start.Unix(), Memory: s.x.Memory,
	})
	idleFor := s.x.Limits.cfg.IdleTimeout
	idle := time.AfterFunc(idleFor, func() { a.cancel(errIdle) })
	defer idle.Stop()
	var seen protocol.Progress
	for {
		st, err := relay.Next()
		if err != nil {
			return a.broke(ctx, relay, err)
		}
		// The upstream is idle when it sends nothing new: a keep-alive, a comment and a repeated snapshot do not
		// count, bytes that make progress on an event and content do (docs/domains/gateway.md「路径与格式」).
		p := relay.Progress()
		if st.Kind == protocol.StepCommit || p.ContentBytes != seen.ContentBytes ||
			p.PartialBytes > seen.PartialBytes || p.Searches != seen.Searches {
			idle.Reset(idleFor)
		}
		seen = p
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

// commit is the first content: from here the status is 200 and nothing is retried (I4). The journal learns of it
// before the client does, so that a crash after the first write still leaves the cleanup the attempt to settle by. A
// client that is gone gets nothing, and the attempt, if it goes on, is not committed.
func (s *session) commit(a *attempt, out []byte) {
	if s.isGone() {
		return
	}
	s.journal(a, true)
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

// put writes b and flushes it, within the write deadline. A client whose context has ended, or a write to it that fails,
// is gone: nothing more is written to it, and an attempt that stops when the client does is stopped now. wmu is held.
func (s *session) put(b []byte) bool {
	if s.gone || s.req.Context().Err() != nil {
		s.gone = true
		return false
	}
	_ = s.rc.SetWriteDeadline(time.Now().Add(s.x.Limits.cfg.WriteTimeout)) //nolint:errcheck // a writer without deadlines is written to as it is
	_, err := s.w.Write(b)                                                 //nolint:gosec // an API response, which is never rendered as a page
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
	cause := context.Cause(ctx)
	e := protocol.Error{Type: protocol.UpstreamFailed}
	finish, again := billing.FinishError, true
	switch {
	case s.isGone():
		finish, again = billing.FinishCancelled, false
	case errors.Is(cause, errIdle):
		e, finish = protocol.Error{Type: protocol.Timeout}, billing.FinishTimeout
	case errors.Is(cause, context.DeadlineExceeded):
		e, finish, again = protocol.Error{Type: protocol.Timeout}, billing.FinishTimeout, false
	case errors.Is(err, protocol.ErrMemory):
		e, again = protocol.Error{Type: protocol.Overloaded}, false
	}
	rec := &s.ends[a.i]
	rec.Usage, rec.Finish = relay.Usage(), finish
	if s.isStarted() {
		rec.Result = billing.Committed
		s.record(a, true, routing.ScopeEndpoint, routing.ReturnToClient)
		return over, ended{relay: relay, err: e}
	}
	rec.Result = billing.FailedBeforeCommit
	drop(relay)
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

// complete handles the upstream's normal end. After the commit point the stream is done and the call settles before it
// ends. Before it, there was no content: a 200 with nothing in it is a failure and the next entry is tried, unless the
// client is gone, whose upstream was read to the end for the usage it will be billed.
func (a *attempt) complete(relay *protocol.Relay) (step, ended) {
	s := a.s
	rec := &s.ends[a.i]
	rec.Usage = relay.Usage()
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

// drop lets go of a relay whose stream is not going to be used: its ending is written for nobody, which gives back
// what it held of the node's memory.
func drop(relay *protocol.Relay) { relay.Fail(protocol.Error{Type: protocol.UpstreamFailed}, nil) }
