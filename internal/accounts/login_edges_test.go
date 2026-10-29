package accounts

import (
	"context"
	"math"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
	"axiom/internal/platform/secret"
)

func TestLoginKeysSeparateSubjects(t *testing.T) {
	t.Parallel()
	key, other := secret.LoginKey{1}, secret.LoginKey{2}
	s := &Service{cache: kvtest.New(t).Open(t, kv.Deps{}), loginKey: key}
	site := SiteID(uuid.New())
	seen := make(map[string]bool)
	for _, subject := range []struct{ kind, value string }{
		{ipKind, "203.0.113.1"}, {emailKind, "203.0.113.1"}, {emailKind, "a@example.com"}, {emailKind, "b@example.com"},
	} {
		got := s.sharedKey(site, subject.kind, subject.value)
		if seen[got] || strings.Contains(got, subject.value) {
			t.Fatalf("subject collision or plaintext in %q", got)
		}
		seen[got] = true
		if s.sharedKey(SiteID(uuid.New()), subject.kind, subject.value) == got {
			t.Fatal("sites share a bucket")
		}
		s.loginKey = other
		if s.sharedKey(site, subject.kind, subject.value) == got {
			t.Fatal("HMAC ignores the login key")
		}
		s.loginKey = key
	}
}

// TestAttemptIPNormalization: the login buckets count a source address as clientip.Group does, which is what the
// gateway's per-address limit counts too, so the two agree on one group: a v4 address in v6 clothes, an IPv6 zone and
// every address of one /64 share one bucket, and another /64 has its own.
func TestAttemptIPNormalization(t *testing.T) {
	t.Parallel()
	s := &Service{}
	site := SiteID(uuid.New())
	l := limits{ipBurst: 2, ipPerMinute: 1}
	group := func(input string) loginLimit[siteIP] {
		return s.ipLimit(site, netip.MustParseAddr(input), l)
	}
	for _, c := range []struct{ name, input, want string }{
		{"IPv4", "203.0.113.1", "203.0.113.1"},
		{"IPv4 in v6 clothes", "::ffff:203.0.113.1", "203.0.113.1"},
		{"IPv6", "2001:db8:1:2:ffff::1", "2001:db8:1:2::"},
		{"IPv6 with a zone", "fe80::1234%eth0", "fe80::"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := group(c.input)
			if got.key.ip != netip.MustParseAddr(c.want) || got.value != c.want {
				t.Fatalf("ipLimit(%s) counts %s / %s, want %s", c.input, got.key.ip, got.value, c.want)
			}
		})
	}
	if got := s.ipLimit(site, netip.Addr{}, l); got.key.ip.IsValid() {
		t.Fatal("an invalid address acquired a bucket")
	}
	first := group("2001:db8:1:2::1")
	for _, input := range []string{"2001:db8:1:2::2", "2001:db8:1:2:ffff::1"} {
		if got := group(input); got.key != first.key || got.value != first.value {
			t.Errorf("%s has its own bucket, want the /64's: %v, want %v", input, got, first)
		}
	}
	if got := group("2001:db8:1:3::1"); got.key == first.key || got.value == first.value {
		t.Errorf("another /64 shares the bucket: %v", got)
	}
}

func TestConcurrentSharedAttemptsStayWithinBurst(t *testing.T) {
	t.Parallel()
	server, namespace := kvtest.New(t), kvtest.Namespace()
	a := &Service{cache: server.OpenIn(t, namespace, kv.Deps{})}
	b := &Service{cache: server.OpenIn(t, namespace, kv.Deps{})}
	key := attemptKey(namespace, "concurrent")
	var accepted atomic.Int64
	var done sync.WaitGroup
	now := time.Unix(1_760_000_000, 0)
	for i := range 64 {
		done.Go(func() {
			s := []*Service{a, b}[i%2]
			wait, ok := s.spendShared(t.Context(), key, 7, time.Hour, now)
			if !ok {
				t.Error("shared store did not answer")
			} else if wait == 0 {
				accepted.Add(1)
			}
		})
	}
	done.Wait()
	if got := accepted.Load(); got != 7 {
		t.Fatalf("accepted %d concurrent attempts, want 7", got)
	}
}

func TestCoarseBucketBoundsSharedKeys(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	now := time.Now()
	s := &Service{cache: server.Open(t, kv.Deps{Now: func() time.Time { return now }}), guards: newGuards(1, time.Second)}
	site := SiteID(uuid.New())
	s.snapshot.Store(&snapshot{sites: map[SiteID]Site{site: {limits: limits{ipBurst: 2, ipPerMinute: 1}}}})
	ip := netip.MustParseAddr("2001:db8::1")
	for range 2 {
		if err := s.fromIP(t.Context(), site, ip); err != nil {
			t.Fatal(err)
		}
	}
	// Emptying the shared bucket must not bypass the node's bound on new keys.
	l := s.ipLimit(site, ip, limits{ipBurst: 2, ipPerMinute: 1})
	key := s.sharedKey(site, l.kind, l.value)
	for range 2 {
		s.giveShared(t.Context(), key, l.per, s.attemptNow())
	}
	if err := s.fromIP(t.Context(), site, netip.MustParseAddr("2001:db8::2")); err == nil {
		t.Fatal("coarse bucket allowed another key-producing request")
	}
}

func TestFallbackShareCannotOverflow(t *testing.T) {
	t.Parallel()
	// A canceled access uses local fallback without changing the real server's state.
	server := kvtest.New(t)
	cfg := server.Config(kvtest.Namespace())
	cfg.Nodes = math.MaxInt
	cache, err := kv.Open(t.Context(), cfg, kvtest.Deps(kv.Deps{}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cache.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	s := &Service{cache: cache, guards: newGuards(1, time.Second)}
	l := s.emailLimit(SiteID{}, "a@example.com", defaultLimits)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, wait := takeAttempt(s, ctx, l); wait != 0 {
		t.Fatal("fallback must leave at least one attempt", wait)
	}
	if _, wait := takeAttempt(s, ctx, l); wait <= 0 {
		t.Fatal("overflow allowed another attempt", wait)
	}
}
