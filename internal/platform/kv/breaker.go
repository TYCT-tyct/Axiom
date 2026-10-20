package kv

import (
	"sync"
	"time"
)

const (
	// windowBuckets is how many one-second buckets the sliding window keeps, so ten seconds of accesses.
	windowBuckets = 10
	// rateAccesses and rateUnavailable are the error-rate trigger: at least this many accesses in the window, and more
	// than half of them unavailable.
	rateAccesses, rateUnavailable = 20, 2
	// slowAccesses and slowShare are the latency trigger: at least this many accesses, and more than a fifth of them
	// ended on our own Timeout. A partial failure slows only part of the traffic, which the error rate cannot see.
	slowAccesses, slowShare = 10, 5
	// streakAccesses is the third trigger: this many unavailable accesses in a row.
	streakAccesses = 5
	// openFor is how long the breaker refuses every access after it opens, with no I/O at all, and again after each
	// failed probe.
	openFor = 5 * time.Second
)

// outcome is how one access ended, as the breaker and the metrics tell them apart (README「熔断」).
type outcome int

const (
	ok          outcome = iota
	unavailable         // a connection error, or the server not answering: ours to fall back on
	timedOut            // Timeout itself expired: also unavailable, and the latency trigger counts it apart
	failed              // the server refused a command or a script: a mistake to fix, not an outage
	skipped             // the caller cancelled or gave up first, which must not push the breaker open
	outcomes            // the count of the above, for the metrics' prebuilt options
)

// bucket is one second of the window.
type bucket struct {
	second      int64 // Unix seconds, what tells this bucket's second from the one before
	accesses    int
	unavailable int
	timeouts    int
}

// breaker is the circuit around the shared state: Valkey's own failures are the only ones that count, and an open
// breaker does no I/O at all, so a frozen server cannot fill the pipeline's ring with abandoned commands
// (README「熔断」).
type breaker struct {
	mu        sync.Mutex
	now       func() time.Time
	buckets   [windowBuckets]bucket
	streak    int
	open      bool
	openUntil time.Time
	probing   bool // a probe is out: the half-open state has at most one
}

func newBreaker(now func() time.Time) *breaker { return &breaker{now: now} }

// allow reports whether an access may do I/O now, and whether it is the half-open probe, whose outcome closes or opens
// the breaker again. The clock can be injected, so a test opens and closes it without waiting.
func (b *breaker) allow(now time.Time) (probe, allowed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return false, true
	}
	if now.Before(b.openUntil) || b.probing {
		return false, false
	}
	b.probing = true
	return true, true
}

// trip opens the breaker for openFor from now, which is what a server that does not answer at startup does.
func (b *breaker) trip(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.openFor(now)
}

// done records how an access ended, and reports whether the breaker changed state and what it is now. A probe's success
// closes the breaker and clears the window, so the accesses that opened it do not open it again at once, and its failure
// opens it again. An access that ends while the breaker is open was started before it opened and says nothing new. A
// skip says nothing about the server at all; a refused command is an answer, which counts as an access and as proof that
// the server is there (README「熔断」).
func (b *breaker) done(started time.Time, probe bool, result outcome) (changed, open bool) {
	if result == skipped {
		return false, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open && !probe {
		return false, true
	}
	was := b.open
	b.record(started, result)
	switch {
	case probe && down(result):
		b.openFor(started)
	case probe:
		b.close()
	case down(result) && (b.streak >= streakAccesses || b.tripped(started)):
		b.openFor(started)
	}
	return b.open != was, b.open
}

// record adds the access to its second's bucket, replacing the bucket that held the same slot ten seconds ago, and
// counts the run of unavailable accesses it continues or ends.
func (b *breaker) record(started time.Time, result outcome) {
	second := started.Unix()
	index := (second%windowBuckets + windowBuckets) % windowBuckets
	if b.buckets[index].second != second {
		b.buckets[index] = bucket{second: second}
	}
	current := &b.buckets[index]
	current.accesses++
	if down(result) {
		current.unavailable++
		b.streak++
	} else {
		b.streak = 0
	}
	if result == timedOut {
		current.timeouts++
	}
}

// down reports a result that says the server is not answering: only these count against it.
func down(result outcome) bool { return result == unavailable || result == timedOut }

// tripped reports whether the window's counts meet one of the two rate triggers. Only the buckets within the window
// count: a slot that has not been written for ten seconds still holds the accesses of its own second.
func (b *breaker) tripped(now time.Time) bool {
	oldest := now.Unix() - windowBuckets + 1
	var accesses, unavailable, timeouts int
	for _, bucket := range b.buckets {
		if bucket.second < oldest || bucket.second > now.Unix() {
			continue
		}
		accesses += bucket.accesses
		unavailable += bucket.unavailable
		timeouts += bucket.timeouts
	}
	if accesses >= rateAccesses && unavailable*rateUnavailable > accesses {
		return true
	}
	return accesses >= slowAccesses && timeouts*slowShare > accesses
}

// openFor refuses every access for openFor from now, and clears the window: the accesses that opened the breaker are
// not evidence about the server that comes back.
func (b *breaker) openFor(now time.Time) {
	b.open, b.openUntil, b.probing = true, now.Add(openFor), false
	b.buckets = [windowBuckets]bucket{}
	b.streak = 0
}

// close lets every access through again.
func (b *breaker) close() {
	b.open, b.probing = false, false
	b.buckets = [windowBuckets]bucket{}
	b.streak = 0
}
