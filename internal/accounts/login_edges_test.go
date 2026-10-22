package accounts

import (
	"context"
	"encoding/hex"
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
)

func TestLoginKeyDerivationAndSubjects(t *testing.T) {
	t.Parallel()
	for _, size := range []int{0, 31, 33} {
		if _, err := deriveLoginKey(make([]byte, size)); err == nil {
			t.Errorf("accepted master key with %d bytes", size)
		}
	}
	master := []byte(strings.Repeat("a", loginKeyLen))
	key, err := deriveLoginKey(master)
	if err != nil {
		t.Fatal(err)
	}
	// A fixed HKDF vector also catches replacing the derivation with the master key itself.
	const want = "93f12231920429a87c22b10d76f68e21a9d71ca18c8805b5fca0bfc102c91538"
	if hex.EncodeToString(key[:]) != want {
		t.Fatal("HKDF label or key changed")
	}
	other, err := deriveLoginKey([]byte(strings.Repeat("b", loginKeyLen)))
	if err != nil || key == other || key == [loginKeyLen]byte(master) {
		t.Fatalf("login key must be derived and change with the master: %v", err)
	}
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

func TestAttemptIPNormalization(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ input, want string }{
		{"203.0.113.1", "203.0.113.1"}, {"::ffff:203.0.113.1", "203.0.113.1"},
		{"2001:db8:1:2:ffff::1", "2001:db8:1:2::"}, {"fe80::1234%eth0", "fe80::"},
	} {
		if got := normalIP(netip.MustParseAddr(c.input)); got != netip.MustParseAddr(c.want) {
			t.Errorf("normalIP(%q) = %s, want %s", c.input, got, c.want)
		}
	}
	if normalIP(netip.Addr()).IsValid() {
		t.Fatal("invalid address became valid")
	}
	s := &Service{guards: newGuards(1, time.Second)}
	now := time.Unix(1_760_000_000, 0)
	for _, ip := range []string{"2001:db8::1", "2001:db8::2", "2001:db8::ffff"} {
		l := s.ipLimit(SiteID{}, netip.MustParseAddr(ip), limits{ipBurst: 2, ipPerMinute: 1})
		wait := l.bucket.take(l.key, l.size, l.per, now)
		if (ip == "2001:db8::ffff") != (wait > 0) {
			t.Fatalf("local bucket for %s: waited %v", ip, wait)
		}
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
