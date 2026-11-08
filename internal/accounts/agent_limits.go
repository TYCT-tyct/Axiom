package accounts

import (
	"slices"
	"time"
)

// SpendAgentRequest spends one of a's requests on this node, of which it may make perMinute a minute and a tenth of
// that at once (600 a minute is 60 at once): past them it is *TooManyAttempts with the wait. perMinute, like the other
// limits below, is the owner's, read for each request, so a change applies to the next one.
func (s *Service) SpendAgentRequest(a Agent, perMinute int) error {
	return s.spendAgent(a, agentLimit{s.agentRequests, max(perMinute/10, 1), time.Minute / time.Duration(perMinute), perMinute, time.Minute})
}

// SpendAgentMoney spends one of a's money writes on this node, besides its request, of which it may do perMinute in any
// minute and perHour in any hour: past either it is *TooManyAttempts with the wait.
func (s *Service) SpendAgentMoney(a Agent, perMinute, perHour int) error {
	return s.spendAgent(a, within(s.agentMoney, perMinute, time.Minute), within(s.agentMoneyHours, perHour, time.Hour))
}

// SpendAgentAlert spends one of a's alerts on this node, of which it may raise perHour in any hour.
func (s *Service) SpendAgentAlert(a Agent, perHour int) error {
	return s.spendAgent(a, within(s.agentAlerts, perHour, time.Hour))
}

// within is a limit of n in any window whose bucket holds half of them at once and gives the rest back over the window
// (30 a minute is 15 at once and one every 4 seconds), so at a steady n the bucket alone keeps to it.
func within(c *agentCounter, n int, window time.Duration) agentLimit {
	size := max(n/2, 1)
	return agentLimit{c, size, window / time.Duration(max(n-size, 1)), n, window}
}

// agentCounter counts one kind of a credential's calls on this node: a token bucket paces them, and the times of the
// last ones granted hold the limit over the window. The bucket alone cannot: it is kept in its refill's units, so after
// the owner lowers the limit it forgets the calls the window already let through and grants more than the new limit.
type agentCounter struct {
	pace    *buckets[AgentID]
	granted *lru[AgentID, []time.Time] // oldest first, at most the limit's n of them
}

func newAgentCounter() *agentCounter {
	return &agentCounter{pace: newBuckets[AgentID](), granted: newLRU[AgentID, []time.Time](cacheLimit)}
}

// agentLimit is one kind of a credential's calls: no more than n in any window, size at once and one more every per.
type agentLimit struct {
	counter *agentCounter
	size    int
	per     time.Duration
	n       int
	window  time.Duration
}

// wait is 0 when fewer than n of id's calls were granted in the window ending at now, otherwise how long until the
// earliest of the last n leaves it.
func (l agentLimit) wait(id AgentID, now time.Time) time.Duration {
	granted, _ := l.counter.granted.get(id, now)
	if len(granted) < l.n {
		return 0
	}
	return granted[len(granted)-l.n].Add(l.window).Sub(now)
}

// grant records a call granted at now, keeping the last n.
func (l agentLimit) grant(id AgentID, now time.Time) {
	granted, _ := l.counter.granted.get(id, now)
	granted = append(granted, now)
	if len(granted) > l.n {
		granted = slices.Clone(granted[len(granted)-l.n:])
	}
	l.counter.granted.put(id, granted, now.Add(l.window))
}

// spendAgent grants one call under every limit or none: past one it is *TooManyAttempts with the wait. agentSpend makes
// the check and the record one step, so two calls at once cannot both see the window's last free place.
func (s *Service) spendAgent(a Agent, limits ...agentLimit) error {
	s.agentSpend.Lock()
	defer s.agentSpend.Unlock()
	now := s.now()
	for _, l := range limits {
		if wait := l.wait(a.id, now); wait > 0 {
			return &TooManyAttempts{RetryAfter: wait}
		}
	}
	for _, l := range limits {
		if wait := l.counter.pace.take(a.id, l.size, l.per, now); wait > 0 {
			return &TooManyAttempts{RetryAfter: wait}
		}
	}
	for _, l := range limits {
		l.grant(a.id, now)
	}
	return nil
}
