package mail

import (
	"strings"
	"sync"
	"time"
)

// limiter allows one message per recipient per interval. It lives in this process for now: Valkey will hold the
// counters across nodes later (#321), and until then each node still keeps one flow from filling an inbox through it.
type limiter struct {
	mu       sync.Mutex
	last     map[string]time.Time // the recipient's last message; keys are lowercased addresses
	now      func() time.Time
	interval time.Duration
}

// newLimiter starts with nobody messaged.
func newLimiter() *limiter {
	return &limiter{last: make(map[string]time.Time), now: time.Now, interval: rateInterval}
}

// allow reports whether the recipient may be messaged now, and records the attempt when it may. Every call also
// drops the recipients of earlier intervals, so the map holds only who was messaged recently rather than everyone
// ever addressed, and an attacker choosing recipients cannot grow it without bound.
func (l *limiter) allow(recipient string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for key, at := range l.last {
		if now.Sub(at) >= l.interval {
			delete(l.last, key)
		}
	}
	// Mail is case-insensitive in practice: one inbox must not get a message per spelling of its address.
	key := strings.ToLower(recipient)
	if _, messaged := l.last[key]; messaged {
		return false
	}
	l.last[key] = now
	return true
}
