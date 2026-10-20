package accounts

import (
	"fmt"
	"math/rand/v2"
	"net/netip"
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
			per := time.Duration(rng.IntN(90)+1) * time.Second
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
	server := kvtest.Own(t) // its server stops between the two steps of the second case
	namespace := kvtest.Namespace()
	// The deadline is the server's answers, not this machine's speed, but the second case reads a server that is not
	// there, and a connection that is not refused at once must not hold the test for a minute.
	s := &Service{cache: server.OpenIn(t, namespace, kv.Deps{AccessTimeout: 2 * time.Second}), guards: newGuards(1, time.Second)}
	site := SiteID(uuid.MustParse("6f1b2f14-3a4c-4bd0-9d1e-2f5a7c8b9d10"))
	limit := func(ip string) loginLimit[siteIP] {
		addr := netip.MustParseAddr(ip)
		return loginLimit[siteIP]{
			bucket: s.guards.ips, key: siteIP{site, addr}, site: site, kind: ipKind, value: addr.String(),
			size: 1, per: time.Minute,
		}
	}
	ctx := t.Context()
	succeed := func() error { return nil }

	shared := limit("198.51.100.1")
	if err := failing(s, ctx, shared, succeed); err != nil {
		t.Fatalf("an attempt in the shared store: %v", err)
	}
	if wait, ok := s.spendShared(ctx, s.sharedKey(site, shared.kind, shared.value), shared.size, shared.per, s.attemptNow()); !ok || wait != 0 {
		t.Errorf("the shared store after an attempt that succeeded: waited %v (answered %v), want the attempt given back", wait, ok)
	}

	server.Stop(t)
	here := limit("198.51.100.2")
	if err := failing(s, ctx, here, succeed); err != nil {
		t.Fatalf("an attempt with the shared store away: %v", err)
	}
	if wait := here.bucket.take(here.key, here.size, here.per, s.attemptNow()); wait != 0 {
		t.Errorf("this node's bucket after an attempt that succeeded while the shared store was away: waited %v, want the attempt given back", wait)
	}
}
