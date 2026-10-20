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
	body, err := readWhole(s.x.Memory, resp.Body, idle, idleFor, &s.replyHeld)
	a.obs.Ended = time.Now()
	if err != nil {
		s.dropReply()
		return s.failedReply(a, ctx, err)
	}
	a.usage = protocol.UsageOf(body)
	a.obs.Done, a.obs.Empty = true, len(body) == 0
	rec := &s.ends[a.i]
	if s.isGone() { // the reply was read to the end for what it says it used, but nothing of it reaches the client
		s.dropReply()
		rec.Result, rec.Finish = billing.FailedBeforeCommit, billing.FinishCancelled
		s.record(a, true, routing.ScopeEndpoint, routing.ReturnToClient)
		return over, ended{}
	}
	rec.Result, rec.Finish = billing.Aggregated, billing.FinishStop
	s.record(a, false, 0, 0)
	return over, ended{reply: body, status: resp.StatusCode}
}

// readWhole reads a reply whole within the node's memory budget, charging each growth of its buffer before it is made,
// and keeps the idle timer from firing while the upstream says something. It holds, and leaves charged, the capacity of
// what it returns, which the caller gives back; held is where that is counted as it grows, so that a read that panics
// leaves it with the caller too. Growing the buffer holds the old one and the new one at once, so the
// node's real peak at a growth is the capacity charged plus the old one, at most twice what was charged (1.5 times it
// for the doubling steps): the charge bounds the reply, not the instant of a copy.
func readWhole(memory *protocol.Memory, body io.Reader, idle *time.Timer, idleFor time.Duration, held *int64) (reply []byte, err error) {
	for {
		if len(reply) == cap(reply) {
			// One byte past the limit: a reply of exactly the limit ends, rather than being taken for a larger one.
			room := min(compactLimit+1-len(reply), max(cap(reply), 64<<10))
			if room <= 0 {
				return reply, protocol.ErrMemory
			}
			if !memory.Charge(int64(room)) {
				return reply, protocol.ErrMemory
			}
			*held += int64(room)
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
			return reply, protocol.ErrMemory
		case errors.Is(err, io.EOF):
			return reply, nil
		case err != nil:
			return reply, err
		}
	}
}

// failedReply ends an attempt whose whole reply could not be read. The client has nothing, so an attempt that fails for
// what the upstream did is given up on for the next entry, as a stream that fails before its commit point is; one that
// fails because the client left, the node is out of memory, or the call is out of road answers with the error.
func (s *session) failedReply(a *attempt, ctx context.Context, err error) (step, ended) {
	a.obs.Cut, a.obs.Fault = cutOf(ctx), protocol.FaultOf(err)
	e, finish, again := s.classify(ctx, err)
	rec := &s.ends[a.i]
	rec.Result, rec.Finish = billing.FailedBeforeCommit, finish
	return s.unanswered(a, e, again)
}
