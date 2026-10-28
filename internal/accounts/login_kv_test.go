package accounts

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"
	"uuid"

	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
)

// attemptKey is one bucket's key in the store of a test, under the namespace the client was opened in: the ACL user
// reaches the ax:* keys and no others (internal/platform/kv/README.md「权限」).
func attemptKey(namespace, key string) string { return "ax:" + namespace + ":login:" + key }

// TestSharedAttemptsMatchTheProcessBuckets: whatever the sequence of takes and gives, the scripts and this node's own
// buckets let the same attempt through at the same moment and refuse the same ones, since they are one algorithm
// (internal/platform/kv/README.md「登录前的尝试次数」).
func TestSharedAttemptsMatchTheProcessBuckets(t *testing.T) {
	t.Parallel()
	namespace := kvtest.Namespace()
	s := &Service{cache: kvtest.New(t).OpenIn(t, namespace, kv.Deps{})}
	for seed := range 8 {
		// A seed's keys are its own: the store keeps what one seed left, and a fresh node's buckets would not.
		prefix := fmt.Sprintf("s%d-", seed)
		keys := []string{prefix + "one", prefix + "two", prefix + "three"}
		rng := rand.New(rand.NewPCG(uint64(seed), 7)) //nolint:gosec // G404: test data, not a secret
		local := newBuckets[string]()
		// The scripts count whole milliseconds, so the moments a test gives them are whole milliseconds too.
		at := time.UnixMilli(1_760_000_000_000)
		for op := range 200 {
			key := keys[rng.IntN(len(keys))]
			per := time.Duration(rng.IntN(90)+1) * time.Minute
			size := rng.IntN(4) + 1
			if rng.IntN(4) == 0 {
				s.giveShared(t.Context(), attemptKey(namespace, key), per, at)
				local.give(key, per, at)
			} else {
				want := local.take(key, size, per, at)
				got, ok := s.spendShared(t.Context(), attemptKey(namespace, key), size, per, at)
				if !ok {
					t.Fatalf("seed %d, op %d: the shared store did not answer", seed, op)
				}
				if got != want {
					t.Fatalf("seed %d, op %d (key %s, burst %d, every %v at %v): the script waited %v, the process %v",
						seed, op, key, size, per, at, got, want)
				}
			}
			at = at.Add(time.Duration(rng.IntN(int(3*per/time.Millisecond))) * time.Millisecond)
		}
	}
}

// TestAttemptsAreGivenBackToTheStoreThatTook: an attempt spent in the shared store is given back there, and one spent in
// this node's bucket is given back here; giving either to the other store would hand a bucket a token it never spent
// (internal/platform/kv/README.md「退回到扣的那一边」).
func TestAttemptsAreGivenBackToTheStoreThatTook(t *testing.T) {
	t.Parallel()
	server := kvtest.New(t)
	namespace := kvtest.Namespace()
	frozen := time.Now()
	s := &Service{cache: server.OpenIn(t, namespace, kv.Deps{Now: func() time.Time { return frozen }}), guards: newGuards(1, time.Second)}
	site := SiteID(uuid.MustParse("6f1b2f14-3a4c-4bd0-9d1e-2f5a7c8b9d10"))
	// A canceled access deterministically uses fallback without a race against a real server's deadline.
	away, cancel := context.WithCancel(t.Context())
	cancel()
	contexts := []context.Context{t.Context(), away}
	rng := rand.New(rand.NewPCG(321, 2)) //nolint:gosec // G404: test data
	for op := range 100 {
		l := s.emailLimit(site, fmt.Sprintf("%d@example.com", op), defaultLimits)
		l.size = 2
		now := s.attemptNow()
		key := s.sharedKey(site, l.kind, l.value)
		if wait, ok := s.spendShared(t.Context(), key, l.size, l.per, now); !ok || wait != 0 {
			t.Fatal("seed the other caller's shared attempt", wait, ok)
		}
		if wait := l.bucket.take(l.key, l.size, l.per, now); wait != 0 {
			t.Fatal("seed the other caller's local attempt", wait)
		}
		takeCtx, giveCtx := contexts[rng.IntN(2)], contexts[rng.IntN(2)]
		at, wait := takeAttempt(s, takeCtx, l)
		if wait != 0 {
			t.Fatalf("op %d: take waited %v", op, wait)
		}
		giveAttempt(s, giveCtx, l, at)
		// Local takes always return locally. A shared take whose give failed stays spent; the other store is untouched.
		sharedWant := 1
		if takeCtx != away && giveCtx == away {
			sharedWant = 0
		}
		assertAttemptsRemaining(t, 1, func() time.Duration { return l.bucket.take(l.key, l.size, l.per, now) })
		assertAttemptsRemaining(t, sharedWant, func() time.Duration {
			wait, ok := s.spendShared(t.Context(), key, l.size, l.per, now)
			if !ok {
				t.Fatalf("op %d: shared store did not answer", op)
			}
			return wait
		})
	}
}

func assertAttemptsRemaining(t *testing.T, n int, take func() time.Duration) {
	t.Helper()
	for range n {
		if wait := take(); wait != 0 {
			t.Fatalf("missing refund: waited %v", wait)
		}
	}
	if wait := take(); wait == 0 {
		t.Fatal("refunded an unspent attempt")
	}
}
