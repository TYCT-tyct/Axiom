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

// sendNext prepares and sends the next hop's request on the same entry and credential. A failure after a search ends
// the call: another entry could not continue without running the searches again on a fresh reservation, and the
// searches already run stay charged.
func (a *attempt) sendNext(ctx context.Context) *ended {
	s := a.s
	if s.isGone() {
		return a.abort(protocol.Error{Type: protocol.UpstreamFailed}, 0)
	}
	// Each hop is its own supplier request, so it takes its own slot and rolling-minute quota. Release the finished
	// request first: with a credential capacity of one, acquiring while the old slot is held would fail even though
	// this call is entitled to reuse it. conn is cleared so a failed acquire cannot call Done twice.
	prev := s.done(a.conn).WithExchange(a.entry.Exchange)
	a.receipts = append(a.receipts, prev)
	a.hopReceipts = append(a.hopReceipts, prev)
	a.conn = nil
	conn, err := s.x.Upstreams.Acquire(uuid.UUID(a.entry.Endpoint))
	if err != nil {
		s.x.Limits.Queue.Wake()
		return a.abort(protocol.Error{Type: protocol.UpstreamFailed}, 0)
	}
	a.conn = conn
	up, held, err := s.prepare(a.entry)
	if err != nil {
		e := s.unprepared(err)
		return &e
	}
	// The next sampling is new paid work: it follows the client even when this endpoint's first call did not. The
	// first hop keeps the attempt context chosen by the endpoint's policy.
	sendCtx := ctx //nolint:contextcheck // the first hop keeps the attempt's context; a loop hop follows the client
	if s.loop != nil {
		sendCtx = s.req.Context()
	}
	resp, fail := a.send(sendCtx, up, held) //nolint:bodyclose // the body is kept in a.nextBody and closed by stream
	if fail != nil {
		s.dropOut()
		_, e := s.failed(a, fail)
		rec := &s.ends[a.i]
		rec.Result, rec.Finish = billing.FailedBeforeCommit, billing.FinishError
		if s.isGone() {
			rec.Finish = billing.FinishCancelled
		}
		if e.err.Type == 0 {
			e.err = protocol.Error{Type: protocol.UpstreamFailed}
		}
		return &e
	}
	a.nextBody, a.nextUsage = resp.Body, up.Usage
	a.seen = protocol.Progress{}
	return nil
}
