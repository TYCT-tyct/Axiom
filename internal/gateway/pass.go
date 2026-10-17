package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"axiom/internal/billing"
	"axiom/internal/protocol"
	"axiom/internal/routing"
)

// compactPath is the Responses compact endpoint's path: the one call an upstream answers whole instead of as a stream
// (docs/domains/gateway.md「转发规则」).
const compactPath = "/v1/responses/compact"

// compactLimit is the most of a compact reply the gateway holds: the cap a reply aggregated from a stream has too. A
// larger body would only be passed on for the client to store, and the node's budget is what reading it would spend.
const compactLimit = 16 << 20

// pass reads the whole reply of an endpoint that does not stream and makes it the call's answer: the body goes to the
// client as it came, after the settlement, and its usage is what the call is charged by. Nothing of it reaches the
// client before it is whole, so an attempt that fails here is still one the call may give up on for another entry.
func (a *attempt) pass(ctx context.Context, resp *http.Response) (step, ended) {
	s := a.s
	idleFor := s.x.Limits.cfg.IdleTimeout
	idle := time.AfterFunc(idleFor, func() { a.cancel(errIdle) })
	defer idle.Stop()
	body, held, err := readWhole(s.x.Memory, resp.Body, idle, idleFor)
	if err != nil {
		s.x.Memory.Refund(held)
		return s.failedReply(a, ctx, err)
	}
	a.usage = protocol.UsageOf(body)
	rec := &s.ends[a.i]
	rec.Result, rec.Finish = billing.Aggregated, billing.FinishStop
	s.record(a, false, 0, 0)
	return over, ended{reply: body, status: resp.StatusCode, held: held}
}

// readWhole reads a reply whole within the node's memory budget, charging each growth of its buffer before it is made,
// and keeps the idle timer from firing while the upstream says something. It holds, and leaves charged, the capacity of
// what it returns, which the caller gives back.
func readWhole(memory *protocol.Memory, body io.Reader, idle *time.Timer, idleFor time.Duration) (reply []byte, held int64, err error) {
	for {
		if len(reply) == cap(reply) {
			// One byte past the limit: a reply of exactly the limit ends, rather than being taken for a larger one.
			room := min(compactLimit+1-len(reply), max(cap(reply), 64<<10))
			if room <= 0 {
				return reply, held, protocol.ErrMemory
			}
			if !memory.Charge(int64(room)) {
				return reply, held, protocol.ErrMemory
			}
			held += int64(room)
			grown := make([]byte, len(reply), cap(reply)+room)
			copy(grown, reply)
			reply = grown
		}
		n, err := body.Read(reply[len(reply):cap(reply)])
		reply = reply[:len(reply)+n]
		if n > 0 {
			idle.Reset(idleFor)
		}
		switch {
		case len(reply) > compactLimit:
			return reply, held, protocol.ErrMemory
		case errors.Is(err, io.EOF):
			return reply, held, nil
		case err != nil:
			return reply, held, err
		}
	}
}

// failedReply ends an attempt whose whole reply could not be read. The client has nothing, so an attempt that fails for
// what the upstream did is given up on for the next entry, as a stream that fails before its commit point is; one that
// fails because the client left, the node is out of memory, or the call is out of road answers with the error.
func (s *session) failedReply(a *attempt, ctx context.Context, err error) (step, ended) {
	rec := &s.ends[a.i]
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
	rec.Result, rec.Finish = billing.FailedBeforeCommit, finish
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
