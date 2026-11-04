package gateway

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"axiom/internal/protocol"
)

// The writes to the client. Every one is made under wmu, which the keep-alive timer and the cancelling of an attempt
// share with the goroutine that runs the call.

// beginReply writes the status and the first bytes of an answer that is not a whole body. A cache replay calls it too:
// its answer has a fresh call ID and its own headers, but the same 200 and the same stream framing.
func (s *session) beginReply(out []byte) {
	if s.isGone() || s.aggregate {
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	h := s.w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	if s.cacheHit {
		h.Set("Cache-Control", "no-store") // an account's cached answer must not be stored by a shared proxy
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	h.Set("X-Accel-Buffering", "no") // an nginx in front would otherwise hold the events back until its buffer fills
	s.relayState()
	s.cacheHeaders(h)
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
	if s.cacheHit {
		h.Set("Cache-Control", "no-store")
	}
	s.relayState()
	if status == http.StatusOK {
		s.cacheHeaders(h)
	}
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
func (s *session) answer(ctx context.Context, e ended, fin *protocol.Final) {
	status, body := protocol.RenderError(s.c.Format, e.err, fin)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.gone || s.started || s.req.Context().Err() != nil {
		return
	}
	if status == http.StatusTooManyRequests { // the only 429 a call ends in after its admission: no credential had a slot
		s.x.Limits.refused(ctx, errCredentialsBusy)
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
