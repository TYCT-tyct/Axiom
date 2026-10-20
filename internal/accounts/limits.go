package accounts

import (
	"context"
	"errors"
	"net/netip"
	"runtime"
	"sync"
	"time"
	"uuid"

	"golang.org/x/sync/semaphore"
)

var (
	// ErrTooManyAttempts is an attempt over one of the site's limits before sign-in: from the IP, failed sign-ins of
	// the email, or failed codes of the user. It is lifted by waiting; no account is locked.
	ErrTooManyAttempts = errors.New("accounts: too many attempts, wait and try again")
	// ErrBusy is a password hash that waited longer than the node allows for a turn to compute it.
	ErrBusy = errors.New("accounts: busy, try again shortly")
)

// TooManyAttempts is ErrTooManyAttempts with how long until the attempt would be let through.
type TooManyAttempts struct{ RetryAfter time.Duration }

func (*TooManyAttempts) Error() string { return ErrTooManyAttempts.Error() }

// Is makes errors.Is(err, ErrTooManyAttempts) true for it.
func (*TooManyAttempts) Is(target error) bool { return target == ErrTooManyAttempts }

// limits are a site's attempt limits before sign-in (README.md「登录前的资源」), its settings over defaultLimits.
type limits struct {
	ipBurst, ipPerMinute int // sign-ins, registrations and two-step verifications from one IP
	emailFailures        int // failed sign-ins of one email per emailWindow
	codeFailures         int // failed TOTP and recovery codes of one user per codeWindow
	challengeAttempts    int // codes per pending session
}

var defaultLimits = limits{ipBurst: 20, ipPerMinute: 10, emailFailures: 10, codeFailures: 10, challengeAttempts: 5}

const (
	emailWindow = 15 * time.Minute
	codeWindow  = time.Hour
	// defaultHashWait is how long a password hash waits for a turn before ErrBusy.
	defaultHashWait = 2 * time.Second
)

// buckets are token buckets by key, each holding size tokens and refilling one every per. A bucket is kept as the
// time it is full again (GCRA), so one that has filled up is the same as none: the entry expires then, and the LRU
// bounds how many are kept. Counted per node, so several nodes multiply a limit by their number.
type buckets[K comparable] struct {
	mu   sync.Mutex // the LRU's get and put are one step here
	full *lru[K, time.Time]
}

func newBuckets[K comparable]() *buckets[K] {
	return &buckets[K]{full: newLRU[K, time.Time](cacheLimit)}
}

// take spends one of key's tokens and returns 0, or spends none and returns how long until one is left.
func (b *buckets[K]) take(key K, size int, per time.Duration, now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	full, ok := b.full.get(key, now)
	if !ok {
		full = now
	}
	if wait := full.Sub(now) + per - time.Duration(size)*per; wait > 0 {
		return wait
	}
	b.full.put(key, full.Add(per), full.Add(per))
	return 0
}

// give returns a token take spent, for an attempt that succeeded: only failures count.
func (b *buckets[K]) give(key K, per time.Duration, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if full, ok := b.full.get(key, now); ok {
		b.full.put(key, full.Add(-per), full.Add(-per))
	}
}

// guards are a node's counters before sign-in and its turns at password hashing.
type guards struct {
	ips    *buckets[siteIP]
	emails *buckets[siteEmail]
	users  *buckets[uuid.UUID]
	keys   *buckets[uuid.UUID] // the call keys an account makes, by account

	hashing  *semaphore.Weighted
	hashWait time.Duration
}

type siteIP struct {
	site SiteID
	ip   netip.Addr
}

type siteEmail struct {
	site  SiteID
	email string // normalized
}

func newGuards(concurrency int, wait time.Duration) guards {
	if concurrency == 0 {
		concurrency = runtime.GOMAXPROCS(0)
	}
	if wait == 0 {
		wait = defaultHashWait
	}
	return guards{
		ips: newBuckets[siteIP](), emails: newBuckets[siteEmail](), users: newBuckets[uuid.UUID](),
		keys:    newBuckets[uuid.UUID](),
		hashing: semaphore.NewWeighted(int64(concurrency)), hashWait: wait,
	}
}

// fromIP spends one of the IP's attempts on site.
func (s *Service) fromIP(site SiteID, ip netip.Addr) error {
	l := s.snapshot.Load().sites[site].limits
	if wait := s.guards.ips.take(siteIP{site, ip}, l.ipBurst, time.Minute/time.Duration(l.ipPerMinute), s.now()); wait > 0 {
		return &TooManyAttempts{RetryAfter: wait}
	}
	return nil
}

// failing runs attempt with one of key's failures spent beforehand, so attempts at once cannot overrun the limit,
// and given back when attempt succeeds.
func failing[K comparable](s *Service, b *buckets[K], key K, size int, window time.Duration, attempt func() error) error {
	per := window / time.Duration(size)
	if wait := b.take(key, size, per, s.now()); wait > 0 {
		return &TooManyAttempts{RetryAfter: wait}
	}
	err := attempt()
	if err == nil {
		b.give(key, per, s.now())
	}
	return err
}

// hash runs an argon2id computation in one of the node's turns, waiting at most hashWait for one; ctx ending while
// it waits returns ctx's error.
func (s *Service) hash(ctx context.Context, compute func()) error {
	wait, cancel := context.WithTimeout(ctx, s.guards.hashWait)
	defer cancel()
	if err := s.guards.hashing.Acquire(wait, 1); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrBusy
	}
	defer s.guards.hashing.Release(1)
	compute()
	return nil
}
