package gateway

import (
	"context"
	"time"
	"uuid"

	"axiom/internal/billing"
	"axiom/internal/platform"
	"axiom/internal/protocol"
)

// conclude settles the call and writes what is left to tell the client. The order is the one that keeps the money
// right: the attempt is over (its upstream cancelled, its read returned) before Settle, and Settle is committed before
// the ending that reports the cost is written (I3). The call's record enters the spill after the settlement and before
// the ending, so a crash after the client has the answer cannot leave the call unrecorded. A settlement not written in
// time goes on in the background once the ending is out; the call's record is then pending, and the background
// completes it when that settlement commits.
func (s *session) conclude(ctx context.Context, e ended) {
	s.concluded = true
	s.stick() //nolint:contextcheck // the queued write outlives the call: Later never waits, and the request's context is often over by now
	o := billing.Outcome{Attempts: s.ends}
	if len(o.Attempts) > 0 {
		addToolUsage(&o.Attempts[s.closeLoop()], s.searches, s.parses, s.pages, s.fileBilling)
	}
	f, err := s.x.settle(ctx, s.c, o, time.Now().Add(s.x.Limits.cfg.SettleRetry))
	var fin *protocol.Final
	if err == nil && (len(o.Attempts) > 0 || s.cacheHit) {
		fin = s.final(f)
	}
	if fin == nil && s.isByok() {
		// The settlement did not reach the response in time, but who served the call is known from the attempt. Without
		// this the pending ending would report is_byok false (docs/domains/gateway.md「用量与费用」).
		fin = &protocol.Final{Pending: true, IsBYOK: true, Files: s.files}
	}
	// The record is in the spill before the ending is written: a process crash after the client sees the answer cannot
	// leave the call unrecorded. A settlement that committed here gives the record its amounts; one left for the
	// background leaves it pending until that settlement commits.
	t, end := s.trail(), time.Now()
	s.x.log(ctx, t, o, f, err != nil, end)
	s.writeEnding(ctx, e, fin, err)
	s.handedOff = s.x.settled(ctx, t, o, end, err)
	if err == nil {
		s.storeCache(ctx)
	}
}

// writeEnding writes what is left to tell the client once the call is settled, with what fin says of the charge: a
// passed-through reply, the error a call that forwarded nothing ends with, an aggregated body, or the end of the stream.
// err is the settlement's, which leaves the ending pending.
func (s *session) writeEnding(ctx context.Context, e ended, fin *protocol.Final, err error) {
	switch {
	case e.reply != nil:
		s.answerReply(ctx, e, fin)
	case e.relay == nil:
		if fin != nil && fin.Cost.Cmp(platform.Exact{}) == 0 {
			fin = nil // an error that cost nothing says nothing of the charge
		}
		s.answer(ctx, e, fin)
	case s.aggregate:
		s.answerAggregate(e, fin, err)
	case e.done:
		final := protocol.Final{Pending: err != nil, IsBYOK: s.isByok(), Files: s.files}
		if fin != nil {
			final = *fin
		}
		s.finish(e.relay.Finish(final))
	default:
		s.finish(e.relay.Fail(e.err, fin))
	}
}

// answerReply writes a passed-through reply with the usage and cost of the settlement (docs/domains/gateway.md
// 「转发规则」), or the error a reply that cannot be rewritten ends in.
func (s *session) answerReply(ctx context.Context, e ended, fin *protocol.Final) {
	var failure protocol.Error
	var failed bool
	if s.c.Format.Direct() {
		e.reply, failure, failed = s.rewriteDirect(e.reply, fin)
	} else {
		e.reply, failure, failed = s.rewriteCompact(e.reply, fin)
	}
	if failed {
		s.answer(ctx, ended{err: failure}, fin)
		return
	}
	s.respond(e.status, e.reply)
}

// storeCache offers the reply this call captured after its settlement succeeded. It is asynchronous and bounded: the
// call has already been answered, so a full queue or a slow database can only lose the cache entry.
func (s *session) storeCache(ctx context.Context) {
	if s.x.Cache == nil || !s.cacheReady || !s.c.cache.enabled || len(s.cacheBody) == 0 {
		return
	}
	s.x.Cache.Offer(ctx, cacheEntry{
		scope: s.c.Admitted.Scope, site: uuid.UUID(s.c.site.ID), account: uuid.UUID(s.c.caller.AccountID()),
		key: s.c.cache.key, source: s.c.ID, format: s.cacheFormat, model: s.cacheModel, state: s.cacheState,
		endpoint: s.cacheEndpoint, body: s.cacheBody, ttl: s.c.cache.ttl,
	})
	s.cacheBody = nil
}

// closeLoop gives every attempt of the model-side loop the last attempt belongs to how the loop ended: its answer
// reached the client, or it did not. It returns the loop's first attempt, which is the last itself outside a loop.
// The searches are counted there, the first of the samplings billing charges together.
func (s *session) closeLoop() int {
	head := len(s.ends) - 1
	result := billing.FailedBeforeCommit
	if r := s.ends[head].Result; r == billing.Committed || r == billing.Aggregated {
		result = r
	}
	for head > 0 && s.ends[head].Continues {
		head--
		s.ends[head].Result = result
	}
	return head
}

// stick moves the session to the endpoint of the charged attempt when that attempt delivered content and the session
// was not already there (docs/domains/routing.md「会话粘性」). A call that delivered nothing writes nothing and forgets
// nothing: the entry stays until its time is up or a later call succeeds elsewhere. It runs before the settlement and the
// ending. The process table is updated immediately; the shared cache is offered a bounded background write, so the
// next request may arrive before that binding is visible (README.md「并发、背压与共享状态」).
func (s *session) stick() {
	if len(s.ends) == 0 {
		return
	}
	last := len(s.ends) - 1
	if r := s.ends[last].Result; r != billing.Committed && r != billing.Aggregated {
		return
	}
	if ep := s.tries[last].entry.Endpoint; ep != s.c.sticky {
		if cache := s.c.stickyCache; cache != nil {
			cache.Later(uuid.UUID(s.c.caller.AccountID()), s.x.Router.StickyWrite(s.c.stickyKey, ep, s.c.Plan.StickyTTL))
		} else {
			s.x.Router.Stick(s.c.Session, ep, s.c.Plan.StickyTTL)
		}
	}
}

// rewriteDirect rewrites a direct format's reply after settlement. Its copy was charged to the node's budget in pass,
// and the body was validated there, so the settlement is never followed by a failure of the call's own making.
func (s *session) rewriteDirect(reply []byte, fin *protocol.Final) ([]byte, protocol.Error, bool) {
	var seen protocol.Usage
	if s.last != nil {
		seen = s.last.usage
	}
	model := s.c.Request.Model
	if s.last != nil {
		model = s.servedModel(s.last)
	}
	body, err := protocol.RewriteDirectReply(s.c.Format, reply, s.c.ID.String(), model, seen, fin)
	if err != nil {
		return nil, protocol.Error{Type: protocol.UpstreamFailed}, true
	}
	return body, protocol.Error{}, false
}

// rewriteCompact rewrites the usage of a compact reply after settlement, so the call reports what it used and was
// charged like every other call. Its copy was charged to the node's budget in pass, and the body was validated there,
// so the settlement is never followed by a failure of the call's own making.
func (s *session) rewriteCompact(reply []byte, fin *protocol.Final) ([]byte, protocol.Error, bool) {
	var seen protocol.Usage
	if s.last != nil {
		seen = s.last.usage
	}
	body, err := protocol.RewriteCompactReply(reply, seen, fin)
	if err != nil {
		return nil, protocol.Error{Type: protocol.UpstreamFailed}, true
	}
	return body, protocol.Error{}, false
}

// answerAggregate writes the whole body of a call that did not stream, after its settlement (I3): the reply the relay
// aggregated, or the failure, which carries what was generated and the error for a Chat or Responses client and is the
// error alone for a Messages one (docs/domains/gateway.md「错误」). Finish and Fail return the body and the status of a
// relay that has not ended yet, which is always the case here. The body is the relay's buffers, so its share of the
// node's budget stays charged until the call is over (session.live), as the compact reply's is (replyHeld).
func (s *session) answerAggregate(e ended, fin *protocol.Final, err error) {
	final := protocol.Final{Pending: err != nil, IsBYOK: s.isByok(), Files: s.files}
	if fin != nil {
		final = *fin
	}
	var body []byte
	if e.done {
		body = e.relay.Finish(final)
	} else {
		if fin != nil && fin.Cost.Cmp(platform.Exact{}) == 0 {
			fin = nil // an error that cost nothing says nothing of the charge
		}
		body = e.relay.Fail(e.err, fin)
	}
	status := e.relay.Status()
	s.respond(status, body)
}

// isByok reports whether the attempt that served the call ran on the account's own key. It reads the last attempt, which
// is the one that served the call, and does not depend on the settlement being written.
func (s *session) isByok() bool {
	n := len(s.tries)
	return n > 0 && s.tries[n-1].entry.Byok
}

// final is what the ending reports of a settlement.
func (s *session) final(f billing.Final) *protocol.Final {
	fin := &protocol.Final{
		Cost: f.Charged, Currency: s.currency, Estimated: f.Estimated,
		ServerToolCost: f.ServerToolCost, CacheDiscount: f.CacheDiscount, Files: s.files,
	}
	fin.IsBYOK = s.isByok()
	for _, seg := range f.Segments {
		fin.Usage.Segments = append(fin.Usage.Segments, protocol.Segment{Usage: seg.Usage, Reported: seg.Reported})
	}
	return fin
}

// settleOnPanic settles a call whose run panicked, with the attempts it had made, and lets the panic go on: the
// reservation must not wait for the cleanup because of a bug.
func (s *session) settleOnPanic(ctx context.Context) {
	p := recover()
	if p == nil {
		return
	}
	if !s.concluded {
		if len(s.ends) > 0 {
			s.closeLoop()
		}
		o := billing.Outcome{Attempts: s.ends}
		f, err := s.x.settle(ctx, s.c, o, time.Now().Add(s.x.Limits.cfg.SettleRetry))
		t, end := s.trail(), time.Now()
		s.x.log(ctx, t, o, f, err != nil, end)
		s.handedOff = s.x.settled(ctx, t, o, end, err)
	}
	panic(p)
}
