package accounts

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strconv"
	"time"
	"uuid"

	"axiom/internal/platform/kv"
)

// The kinds of attempt before sign-in, as the shared store's keys name them (internal/platform/kv/README.md「键」).
const (
	ipKind    = "ip"
	emailKind = "email"
	userKind  = "user"
)

const (
	// loginLabel is what the login key is derived under: a key of its own, so that the API keys' hash key rotating, with
	// two versions at once, does not split one bucket between the nodes (internal/platform/kv/README.md「键」).
	loginLabel = "axiom kv login v1"
	// loginKeyLen is the length of the login key, and of the master key it comes from.
	loginKeyLen = 32
)

// takeScript spends one of a bucket's attempts, and giveScript returns one it spent: the same GCRA as buckets.take and
// buckets.give, over the moment the bucket is full again in milliseconds, so that a bucket that has filled up is no key
// at all (internal/platform/kv/README.md「登录前的尝试次数」).
const takeScript = `
local full, now, interval = tonumber(redis.call('GET', KEYS[1])) or tonumber(ARGV[3]), tonumber(ARGV[3]), tonumber(ARGV[2])
if full < now then full = now end
local wait = full + interval - tonumber(ARGV[1]) * interval - now
if wait > 0 then return {wait} end
full = full + interval
redis.call('SET', KEYS[1], string.format('%d', full), 'PX', string.format('%d', full - now))
return {0}`

const giveScript = `
local full, interval, now = tonumber(redis.call('GET', KEYS[1])), tonumber(ARGV[1]), tonumber(ARGV[2])
if not full then return {0} end
full = full - interval
if full <= now then
  redis.call('DEL', KEYS[1])
else
  redis.call('SET', KEYS[1], string.format('%d', full), 'PX', string.format('%d', full - now))
end
return {0}`

// takeBucket and giveBucket are those scripts, as kv runs them; each answer is a list of one integer, which is how the
// module reads a script's reply.
var (
	takeBucket = kv.NewScript(takeScript)
	giveBucket = kv.NewScript(giveScript)
)

// deriveLoginKey is the HMAC key of the subjects in the login keys: HKDF-SHA256 from the master key's current version.
func deriveLoginKey(master []byte) ([loginKeyLen]byte, error) {
	if len(master) != loginKeyLen {
		return [loginKeyLen]byte{}, errors.New("accounts: a shared cache needs the master key, which the login keys are derived from")
	}
	key, err := hkdf.Key(sha256.New, master, nil, loginLabel, loginKeyLen)
	if err != nil {
		return [loginKeyLen]byte{}, fmt.Errorf("accounts: derive the login key: %w", err)
	}
	return [loginKeyLen]byte(key), nil
}

// shared reports whether the attempts before sign-in are counted in the shared store: without one, or with the cache
// turned off, this node's own buckets hold them (README.md「登录前的资源」).
func (s *Service) shared() bool { return s.cache != nil && s.cache.Enabled() }

// attemptNow is the moment the login buckets count at: the shared store's corrected clock while it is in use, so that
// the nodes agree on it (internal/platform/kv/README.md「时钟」), and this node's own otherwise.
func (s *Service) attemptNow() time.Time {
	if s.shared() {
		return s.cache.Now()
	}
	return s.now()
}

// nodes is the N the fallback divides a limit by: the nodes the cluster has, at least one.
func (s *Service) nodes() int {
	if s.cache == nil {
		return 1
	}
	return s.cache.Nodes()
}

// sharedKey is one bucket's key in the shared store, its subject in HMAC so that a SCAN of the store reads no address and
// no email (internal/platform/kv/README.md「键」); a user's ID goes in as it is, which is not personal data.
func (s *Service) sharedKey(site SiteID, kind, value string) string {
	if kind == userKind {
		return s.cache.Key(kv.LoginSpace, uuid.UUID(site), kind, value)
	}
	mac := hmac.New(sha256.New, s.loginKey[:])
	mac.Write([]byte(kind))
	mac.Write([]byte(value))
	return s.cache.Key(kv.LoginSpace, uuid.UUID(site), kind, hex.EncodeToString(mac.Sum(nil)[:16]))
}

// normalIP is an address as the buckets count it: v4-mapped addresses as their IPv4, and IPv6 by its /64, the block one
// user usually holds, so that changing the address within it buys no new bucket (internal/platform/kv/README.md「键」).
func normalIP(ip netip.Addr) netip.Addr {
	if !ip.IsValid() {
		return ip
	}
	if ip = ip.Unmap(); ip.Is4() {
		return ip
	}
	return netip.PrefixFrom(ip.WithZone(""), 64).Masked().Addr()
}

// millis is a duration as the scripts count it: whole milliseconds, rounded up, so that a bucket refills no sooner than
// the site's setting says.
func millis(d time.Duration) int64 { return (d + time.Millisecond - 1).Milliseconds() }

// loginLimit is one of the counters before sign-in (README.md「登录前的资源」): the bucket this node keeps under key, the
// subject the shared store keeps it under, and the limit both count.
type loginLimit[K comparable] struct {
	bucket *buckets[K] // this node's, which answers while the shared store does not
	key    K
	site   SiteID
	kind   string        // the shared key's field: ip, email or user
	value  string        // the subject, normalized: what the shared key carries hashed, or as it is for a user
	size   int           // attempts in a burst
	per    time.Duration // how long one attempt takes to come back
}

// ipLimit is one source address's attempts on a site, and IP's attempts are never given back.
func (s *Service) ipLimit(site SiteID, ip netip.Addr, l limits) loginLimit[siteIP] {
	ip = normalIP(ip)
	return loginLimit[siteIP]{
		bucket: s.guards.ips, key: siteIP{site, ip}, site: site, kind: ipKind, value: ip.String(),
		size: l.ipBurst, per: time.Minute / time.Duration(l.ipPerMinute),
	}
}

// emailLimit is one email's failed sign-ins on a site.
func (s *Service) emailLimit(site SiteID, email string, l limits) loginLimit[siteEmail] {
	return loginLimit[siteEmail]{
		bucket: s.guards.emails, key: siteEmail{site, email}, site: site, kind: emailKind, value: email,
		size: l.emailFailures, per: emailWindow / time.Duration(l.emailFailures),
	}
}

// userLimit is one user's failed codes on a site.
func (s *Service) userLimit(site SiteID, user uuid.UUID, l limits) loginLimit[siteUser] {
	return loginLimit[siteUser]{
		bucket: s.guards.users, key: siteUser{site, user}, site: site, kind: userKind, value: kv.Hex(user),
		size: l.codeFailures, per: codeWindow / time.Duration(l.codeFailures),
	}
}

// store is where an attempt was spent.
type store uint8

const (
	inNode store = iota // this node's own bucket
	// inShared is the shared store, or one of its keys.
	inShared
)

// spent is an attempt that was spent, and where: giveAttempt takes it back to that store and no other.
type spent struct {
	in       store
	interval time.Duration // the interval of the bucket it was spent in
	key      string        // its key in the shared store, when it was spent there
}

// takeAttempt spends one of l's attempts: in the shared store while it answers, and in this node's own bucket while it
// does not, where the burst is divided by the nodes and the interval multiplied by them, so that N nodes together
// refill no faster than the site says and burst at most N × max(1, burst ÷ N)
// (internal/platform/kv/README.md「不可用」). A wait above zero spends nothing.
func takeAttempt[K comparable](s *Service, ctx context.Context, l loginLimit[K]) (spent, time.Duration) {
	now := s.attemptNow()
	if s.shared() {
		key := s.sharedKey(l.site, l.kind, l.value)
		if wait, ok := s.spendShared(ctx, key, l.size, l.per, now); ok {
			return spent{in: inShared, interval: l.per, key: key}, wait
		}
	}
	nodes := s.nodes()
	interval := time.Duration(math.MaxInt64)
	// N is a deployment setting without an upper bound; overflow must not turn the fallback into a full bucket.
	if int64(nodes) <= math.MaxInt64/int64(l.per) {
		interval = l.per * time.Duration(nodes)
	}
	if wait := l.bucket.take(l.key, max(1, l.size/nodes), interval, now); wait > 0 {
		return spent{}, wait
	}
	return spent{in: inNode, interval: interval}, 0
}

// giveAttempt returns an attempt to the store that spent it: one spent here would hand another node's bucket a token it
// never paid for (internal/platform/kv/README.md「退回到扣的那一边」).
func giveAttempt[K comparable](s *Service, ctx context.Context, l loginLimit[K], at spent) {
	if at.in == inShared {
		s.giveShared(ctx, at.key, at.interval, s.attemptNow())
		return
	}
	l.bucket.give(l.key, at.interval, s.attemptNow())
}

// spendShared spends one of a bucket's attempts in the shared store, in one round trip: it returns how long until one is
// back, and false when the store is not there, which leaves the attempt to this node's bucket.
func (s *Service) spendShared(ctx context.Context, key string, size int, per time.Duration, now time.Time) (time.Duration, bool) {
	values, err := s.cache.Do(ctx, kv.Login, takeBucket.Cmd([]string{key},
		strconv.Itoa(size), strconv.FormatInt(millis(per), 10), strconv.FormatInt(now.UnixMilli(), 10)))
	if err != nil {
		return 0, false
	}
	waits, err := values[0].Ints()
	if err != nil || len(waits) != 1 {
		return 0, false // the script answers one integer; anything else is this node's to count
	}
	return time.Duration(waits[0]) * time.Millisecond, true
}

// giveShared returns an attempt to the shared store. A store that went away between the two steps keeps the attempt
// spent, which counts one failure more instead of handing out a token nobody spent
// (internal/platform/kv/README.md「退回到扣的那一边」).
func (s *Service) giveShared(ctx context.Context, key string, per time.Duration, now time.Time) {
	cmd := giveBucket.Cmd([]string{key}, strconv.FormatInt(millis(per), 10), strconv.FormatInt(now.UnixMilli(), 10))
	_, _ = s.cache.Do(ctx, kv.Login, cmd) //nolint:errcheck // an attempt kept, see above
}

// fromIP spends one of the IP's attempts on site: first the coarse bucket this node keeps, which bounds how many login
// keys one source creates here, and then the bucket itself, shared or this node's own
// (internal/platform/kv/README.md「内存」「登录前的尝试次数」). Single-core mode has no shared keys to guard, and a second
// bucket there would only halve the limit.
func (s *Service) fromIP(ctx context.Context, site SiteID, ip netip.Addr) error {
	l := s.snapshot.Load().sites[site].limits
	limit := s.ipLimit(site, ip, l)
	if s.shared() {
		if wait := s.guards.coarse.take(limit.key, l.ipBurst, limit.per, s.attemptNow()); wait > 0 {
			return &TooManyAttempts{RetryAfter: wait}
		}
	}
	if _, wait := takeAttempt(s, ctx, limit); wait > 0 {
		return &TooManyAttempts{RetryAfter: wait}
	}
	return nil
}

// failing runs attempt with one of l's attempts spent beforehand, so attempts at once cannot overrun the limit, and given
// back to the store that spent it when attempt succeeds: only failures count.
func failing[K comparable](s *Service, ctx context.Context, l loginLimit[K], attempt func() error) error {
	at, wait := takeAttempt(s, ctx, l)
	if wait > 0 {
		return &TooManyAttempts{RetryAfter: wait}
	}
	if err := attempt(); err != nil {
		return err
	}
	giveAttempt(s, ctx, l, at)
	return nil
}
