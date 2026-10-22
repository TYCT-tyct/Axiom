package accounts

import (
	"testing"
	"time"

	"axiom/internal/platform/kv"
	"axiom/internal/platform/kv/kvtest"
)

func TestSharedAttemptKeysExpireAndRefundDeletes(t *testing.T) {
	t.Parallel()
	server := kvtest.Own(t)
	namespace := kvtest.Namespace()
	s := &Service{cache: server.OpenIn(t, namespace, kv.Deps{})}
	key := attemptKey(namespace, "expiry")
	// A very different application clock still produces a relative lifetime, independent of server wall time.
	now := time.Unix(1_760_000_000, 0)
	const per = time.Minute
	s.giveShared(t.Context(), key, per, now)
	checkLoginTTL(t, server, key, 0)
	for range 2 {
		if wait, ok := s.spendShared(t.Context(), key, 2, per, now); !ok || wait != 0 {
			t.Fatal("take", wait, ok)
		}
	}
	checkLoginTTL(t, server, key, 2*per.Milliseconds())
	s.giveShared(t.Context(), key, per, now)
	checkLoginTTL(t, server, key, per.Milliseconds())
	s.giveShared(t.Context(), key, per, now)
	checkLoginTTL(t, server, key, 0)
	if wait, ok := s.spendShared(t.Context(), key, 1, per, now); !ok || wait != 0 {
		t.Fatal("take after full refund", wait, ok)
	}
	// The application's controlled clock makes an old full-at value refill without waiting for physical expiry.
	if wait, ok := s.spendShared(t.Context(), key, 1, per, now.Add(per)); !ok || wait != 0 {
		t.Fatal("take after refill", wait, ok)
	}
	checkLoginTTL(t, server, key, per.Milliseconds())
}

func checkLoginTTL(t *testing.T, server *kvtest.Server, key string, upper int64) {
	t.Helper()
	ttl, err := server.Do(t, t.Context(), server.Admin.B().Pttl().Key(key).Build())[0].AsInt64()
	if err != nil {
		t.Fatal(err)
	}
	if upper == 0 && ttl != -2 || upper > 0 && (ttl <= 0 || ttl > upper) {
		t.Fatalf("TTL = %d, want absent or bounded by %d", ttl, upper)
	}
}
