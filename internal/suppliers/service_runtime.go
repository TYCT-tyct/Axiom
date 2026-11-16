package suppliers

import (
	"sync"
	"sync/atomic"
	"time"
	"uuid"
)

type credRuntime struct {
	configuredMax, configuredMinute int32 // last loaded limits, guarded by runtimeMu, retained when no endpoint routes it
	inflight                        atomic.Int32
	restUnix                        atomic.Int64 // unix nanoseconds until which the credential is not picked
	reporting                       atomic.Bool  // a rejection is queued or being written; reset when the write ends
	requests                        *minuteRuntime
}

// minuteRuntime is the requests one upstream account made in the rolling minute, as this node counted them. It is a
// ring of one-second buckets, each stamped with the second it counts: a bucket from before the window reads as zero,
// so no timer clears anything and memory does not grow with the ceiling. The clock is read while mu is held, so a caller
// delayed between choosing a credential and taking its slot is counted at the second it actually enters, not at the
// earlier time Acquire looked at.
type minuteRuntime struct {
	// The check, the clock read and the increment share a lock so concurrent callers cannot spend the last request
	// twice, or let an earlier caller write after a later one has moved the window.
	mu      sync.Mutex
	now     func() time.Time // the service's clock; nil is time.Now
	started bool
	epoch   time.Time // the first admission; bucket seconds are elapsed from it
	last    int64     // the highest second taken, so a clock that goes back cannot reopen a bucket
	buckets [rpmBuckets]rpmBucket
}

// rpmBucket is one second's requests. second is the runtime's monotonic second the count belongs to, so a slot reused
// by a later second reads as empty without being cleared.
type rpmBucket struct {
	second int64
	count  int32
}

// rpmBuckets is one more than the seconds a rolling minute spans: the buckets for [now-60, now] cover every rolling
// 60-second window that ends now. A request stamped in a second counts until that second is 60 behind, so admission
// under-admits by at most the part of a second before its first request; no rolling window exceeds the ceiling.
const rpmBuckets = 61

// rpmBucketCount caps one second's count. No ceiling is above MaxRequestsPerMinute, so a full bucket refuses every
// finite ceiling, and requests from unlimited credentials cannot overflow the count.
const rpmBucketCount = MaxRequestsPerMinute

// count is a hint shared by an endpoint's credentials; take still decides admission under the lock with a clock read
// at that moment.
func (rt *minuteRuntime) count(now time.Time) int64 {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.sum(rt.second(now))
}

// second is at in the runtime's monotonic seconds, never below the last second taken. It must be called with mu held.
// time.Time.Sub uses the monotonic reading when the clock has one, so a wall-clock step back does not reopen a bucket.
func (rt *minuteRuntime) second(at time.Time) int64 {
	if !rt.started {
		return 0
	}
	elapsed := at.Sub(rt.epoch)
	if elapsed < 0 {
		return rt.last
	}
	s := int64(elapsed / time.Second)
	if s < rt.last {
		return rt.last
	}
	return s
}

// sum is the requests in the buckets for [now-60, now]; buckets stamped with another second are zero.
func (rt *minuteRuntime) sum(now int64) int64 {
	var n int64
	for _, b := range rt.buckets {
		if b.second >= now-rpmBuckets+1 && b.second <= now {
			n += int64(b.count)
		}
	}
	return n
}

// take counts one request at the clock's current time, and says whether it was within the limit; a credential without
// one takes every request. The clock read, sum and increment are one step under mu, so two callers cannot both take the
// last request of a window, and a caller delayed after Acquire cannot count at an earlier second than one that already
// took a request (README.md「并发与速率」). A request counted here is not given back: it may have reached the supplier,
// whose own count it is in.
func (rt *minuteRuntime) take(limit int32) bool { return rt.takeCurrent(limit, nil, nil) }

func (rt *minuteRuntime) takeCurrent(limit int32, s *Service, snap *snapshot) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	now := rt.now
	if now == nil {
		now = time.Now
	}
	at := now()
	// A caller paused before taking the account lock cannot spend a snapshot replaced before a handover was acknowledged.
	if s != nil && s.cfg.Nodes != nil && (s.snap.Load() != snap || !at.Before(snap.until)) {
		return false
	}
	if !rt.started {
		rt.started = true
		rt.epoch = at
	}
	second := rt.second(at)
	if second > rt.last {
		rt.last = second
	}
	if limit != 0 && rt.sum(second)+1 > int64(limit) {
		return false
	}
	b := &rt.buckets[bucketOf(second)]
	if b.second != second { // the slot held a second at least a minute ago; its count has left the window
		*b = rpmBucket{second: second}
	}
	// Unlimited keys still consume their account's quota.
	if b.count < rpmBucketCount {
		b.count++
	}
	return true
}

// bucketOf is the ring slot of a monotonic second.
func bucketOf(second int64) int {
	return int((second%rpmBuckets + rpmBuckets) % rpmBuckets)
}

// ceiling is a per-minute limit as the snapshot holds it: a column with no value is no ceiling of Axiom's, which is
// zero here.
func ceiling(n *int32) int32 {
	if n == nil {
		return 0
	}
	return *n
}

// runtimeOf is the running state of a credential, made when it is first seen and kept after.
func (s *Service) runtimeOf(id, supplier uuid.UUID, max, perMinute int32) *credRuntime {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	rt, ok := s.runtime[id]
	if !ok {
		// new-api middleware/model-rate-limit.go (206e0fa5) and sub2api repository/user_rpm_cache.go (8f2cbb53)
		// count upstream accounts; another key of the same supplier must not create another quota.
		requests := s.minutes[supplier]
		if requests == nil {
			requests = &minuteRuntime{now: s.now}
			s.minutes[supplier] = requests
		}
		rt = &credRuntime{requests: requests}
		s.runtime[id] = rt
	}
	rt.configuredMax, rt.configuredMinute = max, perMinute
	return rt
}
