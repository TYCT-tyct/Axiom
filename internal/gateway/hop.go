package gateway

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/protocol"
)

// serverTool says the relay records calls of the gateway's web search function, false for a call that runs no loop.
func (a *attempt) serverTool() bool {
	return a.s.loop != nil
}

// readHop reads one upstream reply. A hop that calls the web search tool is not the answer: its events were held, the
// searches run, and the next request is prepared; next tells stream to read that request. The answering hop's events
// are written here, so a tool hop never reaches the client, which sees one stream with one ending.
//
//nolint:cyclop,funlen,gocognit // the relay step state machine, one branch per step kind
func (a *attempt) readHop(ctx context.Context, relay *protocol.Relay, idle *time.Timer, idleFor time.Duration) (step, ended, bool) {
	s := a.s
	a.hopRecorded = false
	if s.loop != nil {
		s.dropOut() // a retry starts with no held events from the hop that failed
	}
	for {
		st, err := a.next(relay)
		if err != nil {
			st, e := a.broke(ctx, relay, err)
			return st, e, false
		}
		p := relay.Progress()
		if st.Kind == protocol.StepCommit || p.ContentBytes != a.seen.ContentBytes ||
			p.PartialBytes > a.seen.PartialBytes || p.Searches != a.seen.Searches {
			idle.Reset(idleFor)
		}
		a.seen = p
		if err := a.metered(ctx, p, st.Kind == protocol.StepUpstreamDone, idle, idleFor); err != nil {
			st, e := a.truncate(relay, cut(err))
			return st, e, false
		}
		switch st.Kind {
		case protocol.StepCommit:
			if s.loop != nil && !s.aggregate {
				if !s.holdOut(st.Out) {
					s.dropOut()
					s.drop(relay)
					return over, ended{err: protocol.Error{Type: protocol.Overloaded}, after: 1}, false
				}
				break
			}
			s.commit(a, st.Out)
		case protocol.StepData, protocol.StepHeartbeat:
			if s.loop != nil && !s.aggregate {
				if !s.holdOut(st.Out) {
					s.dropOut()
					s.drop(relay)
					return over, ended{err: protocol.Error{Type: protocol.Overloaded}, after: 1}, false
				}
				break
			}
			s.write(st.Out)
		case protocol.StepUpstreamDone:
			if s.loop == nil {
				st, e := a.complete(relay)
				return st, e, false
			}
			calls := relay.ServerCalls()
			turn := relay.ServerTurn()
			a.hopDone(relay)
			if turn.Client && len(calls) > 0 {
				s.drop(relay)
				return over, *a.abort(protocol.Error{Type: protocol.UpstreamFailed}, 0), false
			}
			if len(calls) > 0 && !s.loop.disabled {
				if s.isGone() {
					s.drop(relay)
					return over, *a.abort(protocol.Error{Type: protocol.UpstreamFailed}, 0), false
				}
				if e := a.search(ctx, turn); e != nil {
					s.drop(relay)
					return over, *e, false
				}
				if e := a.sendNext(ctx); e != nil {
					s.drop(relay)
					return over, *e, false
				}
				s.drop(relay)
				s.dropOut()
				return over, ended{}, true
			}
			if !s.flush(a) {
				rec := &s.ends[a.i]
				if rec.Result == billing.Unknown {
					rec.Result = billing.FailedBeforeCommit
				}
				rec.Finish = billing.FinishCancelled
				s.drop(relay)
				return over, ended{}, false
			}
			st, e := a.complete(relay)
			return st, e, false
		case protocol.StepProgress, protocol.StepBuffered:
		}
	}
}

// flush writes the held events of the answering hop as one stream. Before it the status is not fixed and nothing has
// reached the client (I4); after it the hop is the answer.
func (s *session) flush(a *attempt) bool {
	if len(s.out) == 0 {
		return true
	}
	if s.aggregate {
		// The aggregate reply is the relay's body; the held events only prove the hop answered.
		s.commit(a, nil)
		s.out = nil
		return true
	}
	if s.isGone() {
		s.dropOut()
		return false
	}
	s.commit(a, s.out)
	s.out = nil
	return true
}

// sendNext ends the sampling that asked for the searches and sends the next one on the same entry. Each sampling is a
// supplier request of its own: the finished one gives its slot back and is priced on its own receipt before the next
// takes a slot and its rolling-minute quota, so a credential of capacity one can carry the loop, and each supplier
// charge has its own attempt to reconcile against. A failure after a search ends the call: another entry could not
// continue without running the searches again on a fresh reservation, and the searches already run stay charged.
func (a *attempt) sendNext(ctx context.Context) *ended {
	s := a.s
	if s.isGone() {
		return a.abort(protocol.Error{Type: protocol.UpstreamFailed}, 0)
	}
	s.ends[a.i].Finish = billing.FinishToolUse
	a.endRequest()
	a.hops, a.usage, a.seen = nil, protocol.Usage{}, protocol.Progress{}
	conn, err := s.x.Upstreams.Acquire(uuid.UUID(a.entry.Endpoint))
	if err != nil {
		return a.abort(protocol.Error{Type: protocol.UpstreamFailed}, 0)
	}
	up, held, err := s.prepare(a.entry)
	if err != nil {
		s.done(conn)
		e := s.unprepared(err)
		return a.abort(e.err, e.after)
	}
	if a.i == a.first {
		// The next sampling is new paid work: it follows the client even when the endpoint's policy kept the first one
		// reading after the client left. It stays on the attempt's context, so the deadline, the idle and header waits
		// and the node's shutdown stop it as they stop the first.
		context.AfterFunc(s.req.Context(), func() { a.cancel(errClientGone) })
	}
	a.conn = conn
	a.i = s.begin(a.entry, s.ends[a.i].Entry, true)
	resp, fail := a.send(ctx, up, held) //nolint:bodyclose // the body is kept in a.nextBody and closed by stream
	if fail != nil {
		a.obs.Failure, a.obs.Cut, a.obs.Ended = fail, cutOf(ctx), time.Now()
		s.dropOut()
		_, e := s.failed(a, fail)
		a.endRequest() // after failed: a request the supplier refused unrun costs nothing, one that may have run is estimated
		rec := &s.ends[a.i]
		rec.Finish = billing.FinishError
		if s.isGone() {
			rec.Finish = billing.FinishCancelled
		}
		if e.err.Type == 0 {
			e.err = protocol.Error{Type: protocol.UpstreamFailed}
		}
		return &e
	}
	a.nextBody, a.nextUsage = resp.Body, up.Usage
	return nil
}
