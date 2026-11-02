package gateway

import (
	"bytes"
	"context"
	"io"
	"uuid"

	"axiom/internal/protocol"
)

// cached looks for this call's reply after admission and before the first upstream attempt. A hit replays the stored
// upstream bytes through the same relay a live attempt uses, so format conversion, streaming and the usage ending are
// not a second implementation. A hit that breaks before its commit point is deleted and the call goes on to the
// upstream as a miss; one that breaks after it ends like any other broken stream, but free.
func (s *session) cached(ctx context.Context) (ended, bool) {
	if s.x.Cache == nil || !s.c.cache.enabled {
		return ended{}, false
	}
	account := uuid.UUID(s.c.caller.AccountID())
	if s.c.cache.clear {
		// A clear is a miss even if the delete failed: the successful answer below overwrites the old row, and a read
		// here could otherwise return the entry the client asked to refresh.
		s.x.Cache.Delete(ctx, s.c.Admitted.Scope, account, s.c.cache.key)
		return ended{}, false
	}
	entry, ok := s.x.Cache.Lookup(ctx, s.c.Admitted.Scope, account, s.c.cache.key)
	if !ok {
		return ended{}, false
	}
	return s.replayCached(ctx, account, entry)
}

// replayCached runs a stored answer through the ordinary relay. The stored bytes are the upstream's own, so the client
// gets the same conversion, framing and fresh identifiers it would get from a live attempt; only the usage and cost are
// zero, which RelayOptions.Cached makes the relay write.
func (s *session) replayCached(ctx context.Context, account uuid.UUID, entry *cachedResponse) (ended, bool) {
	now := s.x.Cache.now()
	u := protocol.UsageContextOf(&s.c.Request)
	relay := protocol.NewRelay(s.c.Format, entry.format, bytes.NewReader(entry.body), u, protocol.RelayOptions{
		ID: s.c.ID.String(), Model: s.c.Request.Model, ActualModel: entry.model, Created: s.start.Unix(),
		Memory: s.x.Memory, Aggregate: s.aggregate, Echo: s.echo, ZeroOutput: s.c.Request.ZeroOutput(),
		Cached: true,
	})
	s.live = relay
	s.state = entry.state
	s.cacheHit = true
	s.cacheAge = max(0, now.Sub(entry.createdAt))
	s.cacheTTL = max(0, entry.expiresAt.Sub(now))
	s.cacheSource = entry.source

	committed := false
	for {
		st, err := relay.Next()
		if err != nil {
			return s.replayFailure(ctx, account, relay, committed)
		}
		if s.isGone() {
			s.drop(relay)
			if !committed {
				s.forgetCached()
			}
			return ended{err: protocol.Error{Type: protocol.Timeout}}, true
		}
		switch st.Kind {
		case protocol.StepCommit:
			committed = true
			s.beginReply(st.Out)
		case protocol.StepData:
			s.write(st.Out)
		case protocol.StepProgress, protocol.StepBuffered, protocol.StepHeartbeat:
			// A stored reply has no relay-only progress to forward.
		case protocol.StepUpstreamDone:
			if !committed {
				s.cacheMiss(ctx, account, relay)
				return ended{}, false
			}
			return ended{relay: relay, done: true}, true
		}
	}
}

// cacheMiss drops a stored answer that could not be replayed before its commit point, so the call can go upstream and
// a later successful attempt can replace it.
func (s *session) cacheMiss(ctx context.Context, account uuid.UUID, relay *protocol.Relay) {
	s.drop(relay)
	s.forgetCached()
	s.cacheTTL = s.c.cache.ttl
	s.x.Cache.Delete(ctx, s.c.Admitted.Scope, account, s.c.cache.key)
	s.x.Cache.count(ctx, "replay_error")
}

// forgetCached removes the headers and log state of a hit that will not answer the call.
func (s *session) forgetCached() {
	s.cacheHit = false
	s.state = ""
	s.cacheAge = 0
	s.cacheSource = uuid.UUID{}
}

// cacheCaptureChunk is what each growth of a captured reply charges the node's budget before it allocates. A reply that
// grows past maxCacheEntry stops being captured and is simply not cached; the upstream read goes on.
const cacheCaptureChunk = 64 << 10

// cacheCapture wraps an upstream body and keeps a bounded copy of it. The copy is charged to the node's budget, so a
// cacheable call cannot take more memory than one that is not.
type cacheCapture struct {
	io.ReadCloser
	s        *session
	buf      []byte
	overflow bool
}

func newCacheCapture(s *session, body io.ReadCloser) *cacheCapture {
	if !s.x.Memory.Charge(cacheCaptureChunk) {
		return nil
	}
	s.cacheHeld += cacheCaptureChunk
	return &cacheCapture{ReadCloser: body, s: s, buf: make([]byte, 0, cacheCaptureChunk)}
}

func (c *cacheCapture) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if n > 0 && !c.overflow {
		c.add(p[:n])
	}
	return n, err
}

func (c *cacheCapture) add(p []byte) {
	if len(c.buf)+len(p) > maxCacheEntry {
		c.abandon()
		return
	}
	for cap(c.buf) < len(c.buf)+len(p) {
		grow := min(cacheCaptureChunk, maxCacheEntry-cap(c.buf))
		if grow <= 0 || !c.s.x.Memory.Charge(int64(grow)) {
			c.abandon()
			return
		}
		c.s.cacheHeld += int64(grow)
		next := make([]byte, len(c.buf), cap(c.buf)+grow)
		copy(next, c.buf)
		c.buf = next
	}
	c.buf = append(c.buf, p...)
}

// abandon gives the budget back when the reply will not fit; the rest of the stream is read without being kept.
func (c *cacheCapture) abandon() {
	c.overflow = true
	c.buf = nil
	if c.s.cacheHeld > 0 {
		c.s.x.Memory.Refund(c.s.cacheHeld)
		c.s.cacheHeld = 0
	}
}

// capture takes ownership of a completed attempt's bytes for the write after settlement.
func (s *session) capture(a *attempt, c *cacheCapture) {
	if c == nil || c.overflow || !a.content || len(c.buf) == 0 || s.cacheReady || s.isGone() {
		return
	}
	s.cacheReady = true
	s.cacheBody = c.buf
	c.buf = nil
	s.cacheFormat = a.entry.Target.Format
	s.cacheModel = s.servedModel(a)
	s.cacheState = a.state
}

// replayFailure turns a relay error into a miss before the commit point and a broken stream after it.
func (s *session) replayFailure(ctx context.Context, account uuid.UUID, relay *protocol.Relay, committed bool) (ended, bool) {
	if !committed {
		s.cacheMiss(ctx, account, relay)
		return ended{}, false
	}
	s.x.Cache.count(ctx, "replay_error")
	return ended{relay: relay, err: protocol.Error{Type: protocol.UpstreamFailed}}, true
}
