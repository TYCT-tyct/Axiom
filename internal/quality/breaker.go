package quality

import (
	"errors"
	"time"
	"uuid"

	"axiom/internal/suppliers"
)

// Mode controls whether health evidence changes routes. The zero value only observes.
type Mode string

// The global health modes.
const (
	Observe Mode = "observe"
	Enforce Mode = "enforce"
)

const (
	windowSize = 32
	windowIdle = 10 * time.Minute
	probeBase  = 30 * time.Second
	probeMax   = 10 * time.Minute
	slowStart  = time.Minute
)

type sample struct {
	source, credential uuid.UUID
	verdict            verdict
}
type window struct {
	samples [windowSize]sample
	n, next int
	last    time.Time
}

func (w *window) add(v verdict, o *Observation) {
	if o.Ended.Sub(w.last) > windowIdle {
		*w = window{}
	}
	w.samples[w.next] = sample{o.Source, o.Credential, v}
	w.next = (w.next + 1) % windowSize
	w.n = min(w.n+1, windowSize)
	w.last = o.Ended
}

func (w *window) evidence(credentials int) (failures, sources int, trip bool) {
	if w.n < 5 || !w.consecutive(3) {
		return
	}
	var all, failed, keys [windowSize]uuid.UUID
	var na, nf, nk int
	hard := false
	for i := 0; i < w.n; i++ {
		s := w.samples[i]
		na = unique(&all, na, s.source)
		if s.verdict == success {
			continue
		}
		failures++
		nf = unique(&failed, nf, s.source)
		nk = unique(&keys, nk, s.credential)
		hard = hard || s.verdict == failure
	}
	sources = nf
	threshold, consecutive := max(5, (w.n+1)/2), 3
	independent := nf >= 2
	if na == 1 {
		threshold, consecutive = max(10, (3*w.n+3)/4), 5
		independent = true
	}
	if failures < threshold || !hard || !independent || (credentials > 1 && nk < 2) {
		return
	}
	return failures, sources, w.consecutive(consecutive)
}

func (w *window) consecutive(n int) bool {
	for i := range n {
		if w.samples[(w.next-1-i+windowSize)%windowSize].verdict == success {
			return false
		}
	}
	return true
}

func unique(ids *[windowSize]uuid.UUID, n int, id uuid.UUID) int {
	for i := range n {
		if ids[i] == id {
			return n
		}
	}
	ids[n] = id
	return n + 1
}

type healthRecord struct {
	ID                                  uuid.UUID
	Epoch                               int64
	Opened, Restored, Next, Last, Lease int64
	Backoff, Failures, Sources          int
	Cause, Result                       string
	Request, Owner, Credential          uuid.UUID
	Warned                              bool
	Mode                                Mode
}

type change struct {
	generation uint64
	probeOwner uuid.UUID
	probeLease int64
	before     int64
	record     healthRecord
	kind       string
}

// Live is the endpoint's shared state across routing snapshots. credentials is the number of usable keys.
func (m *Monitor) Live(id suppliers.EndpointID, credentials int) *State {
	e := m.of(id)
	e.mu.Lock()
	e.credentials = credentials
	e.mu.Unlock()
	return e
}

// Routing reads the live breaker and the temporary concurrency ceiling; zero means closed/unlimited.
func (e *State) Routing(now time.Time) (opened time.Time, limit int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.mode != Enforce {
		return
	}
	if e.record.Opened != 0 {
		return time.Unix(0, e.record.Opened), 0
	}
	elapsed := now.Sub(time.Unix(0, e.record.Restored))
	if e.record.Restored != 0 && elapsed >= 0 && elapsed < slowStart {
		limit = 1 << uint(elapsed/(5*time.Second))
	}
	return
}

func (m *Monitor) health(e *State, v verdict, o *Observation) {
	if v == uncounted {
		return
	}
	r := e.record
	if r.Opened != 0 {
		m.recover(e, v, o)
		return
	}
	if o.Source == (uuid.UUID{}) || r.Restored != 0 && o.Sent.UnixNano() <= r.Restored {
		return
	}
	if e.steady(v, o.Ended) && r.Backoff > 0 {
		r.Backoff = 0
	}
	e.window.add(v, o)
	failures, sources, trip := e.window.evidence(e.credentials)
	if !trip {
		if e.record.Backoff != r.Backoff {
			m.transition(e, r, "", false)
		}
		return
	}
	r.ID = e.id
	r.Warned = false
	r.Opened = o.Ended.UnixNano()
	r.Restored = 0
	r.Next = o.Ended.Add(m.delay(r.Backoff)).UnixNano()
	r.Failures = failures
	r.Sources = sources
	r.Request = o.Request
	r.Mode = m.mode
	r.Cause = removalCause(o)
	m.transition(e, r, "removed", false)
}

// A full handoff delays a transition until another counted outcome; it never drops an accepted transition.
func (m *Monitor) transition(e *State, r healthRecord, kind string, probe bool) bool {
	r.ID = e.id
	r.Epoch = e.record.Epoch + 1
	r.Mode = m.mode
	if kind == "removed" && e.owned {
		kind = "owned_endpoint_removed"
	}
	if m.mode != Enforce {
		switch kind {
		case "removed", "owned_endpoint_removed":
			kind = "would_remove"
		case "restored":
			kind = "would_restore"
		}
	}
	c := change{generation: e.generation, before: e.record.Epoch, record: r, kind: kind}
	if probe {
		c.probeOwner = e.record.Owner
		c.probeLease = e.record.Lease
	}
	select {
	case m.changes <- c:
		e.record = r
		e.pending++
		return true
	default:
		m.deferred.Add(1)
		return false
	}
}

func (m *Monitor) delay(level int) time.Duration {
	d := min(probeBase*time.Duration(1<<uint(min(level, 5))), probeMax)
	if m.loop == nil {
		return d
	}
	return max(probeBase, min(time.Duration(float64(d)*(.75+.5*m.loop.rand())), probeMax))
}

// recover closes an observe-mode would-remove on a counted success. Enforce mode restores only through a probe: a real
// request is never sent to a tripped endpoint, and one admitted before the trip says nothing about the endpoint now.
func (m *Monitor) recover(e *State, v verdict, o *Observation) {
	if v != success || m.mode == Enforce {
		return
	}
	r := e.record
	r.Opened = 0
	r.Restored = o.Ended.UnixNano()
	r.Next = 0
	r.Owner = uuid.UUID{}
	r.Lease = 0
	r.Request = o.Request
	r.Warned = false
	if m.transition(e, r, "restored", false) {
		e.window = window{}
		e.successes = 0
	}
}

func removalCause(o *Observation) string {
	switch {
	case o.Failure != nil:
		return o.Failure.Cause.String()
	case o.Cut != NotCut:
		return "timeout"
	case o.Empty:
		return "empty_stream"
	default:
		return "broken_stream"
	}
}

func (e *State) steady(v verdict, at time.Time) bool {
	if v == success {
		e.successes++
	} else {
		e.successes = 0
	}
	return e.successes >= 10 || e.record.Restored != 0 && at.Sub(time.Unix(0, e.record.Restored)) >= windowIdle
}

// ParseMode owns the default and validation for the environment and embedded call paths.
func ParseMode(value string) (Mode, error) {
	if value == "" {
		return Observe, nil
	}
	mode := Mode(value)
	if mode != Observe && mode != Enforce {
		return "", errors.New("want observe or enforce")
	}
	return mode, nil
}
